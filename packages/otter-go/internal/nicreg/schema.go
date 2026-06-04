// Package nicreg holds HTTP handlers for /api/v1/nic-registrations/* — the
// end-user (internal DoD customer) registration intake. Customers submit
// registrations shaped by the DoD NIC templates; a NIC reviewer approves or
// rejects and records whether the registration should flow upstream to ARIN
// (push_to_arin). First cut is form-capture only: data is captured and the
// decision recorded; rendering NIC template text and acting on push_to_arin
// (via internal/lir/arin) are later milestones.
//
// The field schema is the canonical templates.json, embedded here and synced
// into finch (src/nic/templates.gen.json) so the dynamic form and this
// validator can never disagree. Human field-help + emit layouts live in
// docs/nic-templates/.
package nicreg

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

//go:embed templates.json
var schemaBytes []byte

// Schema is the parsed templates.json. Loaded once at package init.
type Schema struct {
	Version   int                       `json:"version"`
	Enums     map[string][]EnumOption   `json:"enums"`
	Templates map[string]TemplateSchema `json:"templates"`
}

type EnumOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type TemplateSchema struct {
	NicID        string    `json:"nicId"`
	Label        string    `json:"label"`
	Table        string    `json:"table"`
	Actions      []string  `json:"actions"`
	ArinEligible bool      `json:"arinEligible"`
	Sections     []Section `json:"sections"`
}

type Section struct {
	Title       string     `json:"title"`
	Help        string     `json:"help"`
	VisibleWhen *Condition `json:"visibleWhen"`
	Fields      []Field    `json:"fields"`
}

type Field struct {
	Key                string       `json:"key"`
	Label              string       `json:"label"`
	Type               string       `json:"type"`
	Required           bool         `json:"required"`
	RequiredForActions []string     `json:"requiredForActions"`
	Help               string       `json:"help"`
	MaxLength          int          `json:"maxLength"`
	Min                *int         `json:"min"`
	Max                *int         `json:"max"`
	Pattern            string       `json:"pattern"`
	Options            []EnumOption `json:"options"`
	EnumRef            string       `json:"enumRef"`
	VisibleWhen        *Condition   `json:"visibleWhen"`
	Repeat             *Repeat      `json:"repeat"`
}

type Condition struct {
	Field  string `json:"field"`
	Equals string `json:"equals"`
}

type Repeat struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

var schema Schema

func init() {
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		// Embedded asset is part of the binary — a parse failure is a
		// build-time mistake, not a runtime condition. Fail loudly.
		panic(fmt.Sprintf("nicreg: invalid embedded templates.json: %v", err))
	}
}

// SchemaBytes returns the raw embedded templates.json (served to the frontend
// for parity / debugging).
func SchemaBytes() []byte { return schemaBytes }

// Template returns the schema for a template type, or ok=false if unknown.
func Template(t string) (TemplateSchema, bool) {
	ts, ok := schema.Templates[t]
	return ts, ok
}

// ActionAllowed reports whether action (N/M/D/R) is valid for the template.
func (ts TemplateSchema) ActionAllowed(action string) bool {
	for _, a := range ts.Actions {
		if a == action {
			return true
		}
	}
	return false
}

// options resolves a field's allowed enum values (inline options or enumRef).
func (f Field) options() []EnumOption {
	if len(f.Options) > 0 {
		return f.Options
	}
	if f.EnumRef != "" {
		return schema.Enums[f.EnumRef]
	}
	return nil
}

// condMet reports whether a visibleWhen condition holds for the payload.
// A nil condition is always met.
func condMet(c *Condition, payload map[string]any) bool {
	if c == nil {
		return true
	}
	return asString(payload[c.Field]) == c.Equals
}

// Validate checks a payload against a template's schema for the given action.
// It enforces: only-known keys are tolerant (unknown keys ignored), required
// fields present (honoring requiredForActions + conditional visibility), enum
// membership, max length, and repeat min/max. Returns a *ValidationError with
// all problems joined, or nil when valid.
func Validate(templateType, action string, payload map[string]any) error {
	ts, ok := schema.Templates[templateType]
	if !ok {
		return &ValidationError{Msg: "unknown template_type: " + templateType}
	}
	if !ts.ActionAllowed(action) {
		return &ValidationError{Msg: fmt.Sprintf("action %q not allowed for %s", action, templateType)}
	}
	var problems []string
	for _, sec := range ts.Sections {
		if !condMet(sec.VisibleWhen, payload) {
			continue
		}
		for _, f := range sec.Fields {
			if msg := checkField(f, action, payload); msg != "" {
				problems = append(problems, msg)
			}
		}
	}
	if len(problems) > 0 {
		return &ValidationError{Msg: strings.Join(problems, "; ")}
	}
	return nil
}

// checkField validates one field in a visible section, returning a problem
// string ("" when ok). Hidden fields and absent-but-optional fields pass.
func checkField(f Field, action string, payload map[string]any) string {
	if !condMet(f.VisibleWhen, payload) {
		return ""
	}
	raw, present := payload[f.Key]
	empty := !present || isEmptyValue(raw)
	if empty {
		if fieldRequired(f, action) {
			return f.Label + " is required"
		}
		return ""
	}
	return validateValue(f, raw)
}

func fieldRequired(f Field, action string) bool {
	if f.Required {
		return true
	}
	for _, a := range f.RequiredForActions {
		if a == action {
			return true
		}
	}
	return false
}

func validateValue(f Field, raw any) string {
	if f.Repeat == nil {
		return validateScalar(f, raw)
	}
	arr, ok := raw.([]any)
	if !ok {
		return f.Label + " must be a list"
	}
	// Blank entries are dropped before insert (stringSlice), so min/max
	// must be measured against non-empty values — otherwise a required
	// repeat field submitted as [""] would slip past the min check and
	// store an empty array.
	nonEmpty := 0
	for _, el := range arr {
		if isEmptyValue(el) {
			continue
		}
		nonEmpty++
		if msg := validateScalar(f, el); msg != "" {
			return msg
		}
	}
	if nonEmpty < f.Repeat.Min {
		return fmt.Sprintf("%s requires at least %d value(s)", f.Label, f.Repeat.Min)
	}
	if f.Repeat.Max > 0 && nonEmpty > f.Repeat.Max {
		return fmt.Sprintf("%s allows at most %d value(s)", f.Label, f.Repeat.Max)
	}
	return ""
}

func validateScalar(f Field, raw any) string {
	switch f.Type {
	case "enum":
		return validateEnum(f, raw)
	case "int":
		return validateInt(f, raw)
	case "bool":
		if _, ok := raw.(bool); !ok {
			return f.Label + " must be true/false"
		}
		return ""
	case "date":
		if _, ok := parseDateString(asString(raw)); !ok {
			return f.Label + " must be a date (yyyymmdd or yyyy-mm-dd)"
		}
		return ""
	default: // string, text, email, phone, ip
		s := asString(raw)
		if f.MaxLength > 0 && len(s) > f.MaxLength {
			return fmt.Sprintf("%s exceeds max length %d", f.Label, f.MaxLength)
		}
		return ""
	}
}

func validateEnum(f Field, raw any) string {
	v := asString(raw)
	for _, o := range f.options() {
		if o.Value == v {
			return ""
		}
	}
	return f.Label + " has an invalid value"
}

func validateInt(f Field, raw any) string {
	n, ok := asFloat(raw)
	if !ok {
		return f.Label + " must be a number"
	}
	if n != math.Trunc(n) {
		return f.Label + " must be a whole number"
	}
	if f.Min != nil && n < float64(*f.Min) {
		return fmt.Sprintf("%s must be >= %d", f.Label, *f.Min)
	}
	if f.Max != nil && n > float64(*f.Max) {
		return fmt.Sprintf("%s must be <= %d", f.Label, *f.Max)
	}
	return ""
}

func isEmptyValue(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(t) == ""
	case []any:
		return len(t) == 0
	default:
		return false
	}
}

// ValidationError surfaces as HTTP 422 with its message.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }
