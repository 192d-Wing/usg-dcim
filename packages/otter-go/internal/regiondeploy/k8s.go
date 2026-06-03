// Minimal Kubernetes API client for the regiondeploy kubeconfig callback.
// Ports the subset of dcim.regiondeploy.k8s the callback actually exercises:
// in-pod service-account auth (token + CA from the well-known kubelet paths)
// plus Secret create-or-replace. Server-side-apply of Tinkerbell/Rufio CRs is
// out of scope until the `start` endpoint lands; that's the orchestrator's
// job, not the callback's.
//
// Why net/http instead of k8s.io/client-go: same reasoning Python carries
// — pulling client-go in for one Secret POST drags informer/watch/openapi
// surface area we'd never use. The K8s Secret API is six lines of JSON.
package regiondeploy

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// Well-known paths the kubelet injects into every pod with a service
// account mounted. Constants so tests can monkey-patch via env if
// needed (not exposed today; add when a real test case needs it).
const (
	saTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	saCAPath    = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
)

// k8sSecretWriter is the interface the callback handler uses to write
// the kubeconfig Secret. Production wires K8sClient.from_in_pod;
// tests inject a fake that records the calls. Returns nil error on
// success, otherwise an error carrying enough context for the failure
// event row.
type k8sSecretWriter interface {
	CreateOrReplaceSecret(ctx context.Context, namespace, name string, data map[string]string, labels map[string]string) error
}

// K8sClient is a tiny in-pod K8s API client. Holds an *http.Client
// configured with the SA bearer token + CA bundle and the API server's
// base URL. Construct via NewInPodK8sClient.
type K8sClient struct {
	apiServer string
	token     string
	hc        *http.Client
}

// NewInPodK8sClient reads the SA token + CA bundle from the kubelet
// well-known paths and assembles a client pointing at the in-cluster
// API server. Returns an error when KUBERNETES_SERVICE_HOST is unset
// (caller is not in-pod) or the token file is unreadable — the
// callback handler treats either as "K8s write skipped" rather than
// surfacing a 500 (parity with Python's try/except OSError, RuntimeError).
func NewInPodK8sClient() (*K8sClient, error) {
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	if host == "" {
		return nil, errors.New("KUBERNETES_SERVICE_HOST not set; not running in-pod?")
	}
	port := os.Getenv("KUBERNETES_SERVICE_PORT")
	if port == "" {
		port = "443"
	}
	tokenBytes, err := os.ReadFile(saTokenPath)
	if err != nil {
		return nil, fmt.Errorf("read SA token: %w", err)
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if caBytes, err := os.ReadFile(saCAPath); err == nil {
		pool := x509.NewCertPool()
		if pool.AppendCertsFromPEM(caBytes) {
			tlsCfg.RootCAs = pool
		} else {
			// CA file existed but didn't parse — fall back to InsecureSkipVerify
			// rather than fail closed. Production clusters always parse OK;
			// this branch is the dev-cluster self-signed quirk Python's
			// `verify=False` branch handled.
			tlsCfg.InsecureSkipVerify = true
		}
	} else {
		// No CA bundle (probably dev). Python silently sets verify=False here.
		tlsCfg.InsecureSkipVerify = true
	}
	return &K8sClient{
		apiServer: fmt.Sprintf("https://%s:%s", host, port),
		token:     string(bytes.TrimSpace(tokenBytes)),
		hc: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		},
	}, nil
}

// CreateOrReplaceSecret writes a core/v1 Secret idempotently: POSTs
// first, falls back to PUT on 409. Data values are base64-encoded
// per K8s API expectations; labels land on metadata.labels for the
// `dcim.region-deployment=<id>` selector ops use for cleanup.
func (c *K8sClient) CreateOrReplaceSecret(ctx context.Context, namespace, name string, data, labels map[string]string) error {
	body := buildSecretBody(name, data, labels)
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal secret body: %w", err)
	}
	status, respBody, err := c.do(ctx, http.MethodPost, secretsCollectionURL(namespace), raw)
	if err != nil {
		return err
	}
	if status >= 200 && status < 300 {
		return nil
	}
	if status != http.StatusConflict {
		return apiError(status, respBody)
	}
	// 409 → resource exists; fall back to PUT for idempotent replace.
	status, respBody, err = c.do(ctx, http.MethodPut, secretItemURL(namespace, name), raw)
	if err != nil {
		return err
	}
	if status >= 200 && status < 300 {
		return nil
	}
	return apiError(status, respBody)
}

func (c *K8sClient) do(ctx context.Context, method, urlPath string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.apiServer+urlPath, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, nil
}

func buildSecretBody(name string, data, labels map[string]string) map[string]any {
	encoded := make(map[string]string, len(data))
	for k, v := range data {
		encoded[k] = base64.StdEncoding.EncodeToString([]byte(v))
	}
	if labels == nil {
		labels = map[string]string{}
	}
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   map[string]any{"name": name, "labels": labels},
		"type":       "Opaque",
		"data":       encoded,
	}
}

func secretsCollectionURL(namespace string) string {
	return "/api/v1/namespaces/" + namespace + "/secrets"
}

func secretItemURL(namespace, name string) string {
	return "/api/v1/namespaces/" + namespace + "/secrets/" + name
}

func apiError(status int, body []byte) error {
	return fmt.Errorf("k8s api %d: %s", status, string(body))
}
