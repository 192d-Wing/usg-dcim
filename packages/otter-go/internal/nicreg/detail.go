package nicreg

import (
	"context"
	"time"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// insertDetail writes the typed detail row for a registration, mapping the
// validated payload (a JSON any-tree) into the template's sqlc Create params.
// Runs on whatever Querier the caller provides (tx-bound on the create path).
// Returns the created detail row for the response body.
func insertDetail(ctx context.Context, q Querier, regID uuid.UUID, templateType string, p map[string]any) (any, error) {
	switch templateType {
	case "organization":
		return q.CreateNicRegOrganization(ctx, dbq.CreateNicRegOrganizationParams{
			RegistrationID:   regID,
			Agency:           reqString(p, "agency"),
			PrimaryOrgPoc:    ptrString(p, "primary_org_poc"),
			SecondaryOrgPoc:  ptrString(p, "secondary_org_poc"),
			OrganizationName: reqString(p, "organization_name"),
			AddressLine1:     reqString(p, "address_line1"),
			AddressLine2:     ptrString(p, "address_line2"),
			AddressLine3:     ptrString(p, "address_line3"),
			AddressLine4:     ptrString(p, "address_line4"),
			City:             reqString(p, "city"),
			StateCode:        reqString(p, "state_code"),
			ZipCode:          ptrString(p, "zip_code"),
			OrgMailbox:       ptrString(p, "org_mailbox"),
			UserComments:     ptrString(p, "user_comments"),
		})
	case "user":
		return q.CreateNicRegUser(ctx, dbq.CreateNicRegUserParams{
			RegistrationID:     regID,
			LastName:           reqString(p, "last_name"),
			FirstName:          reqString(p, "first_name"),
			MiddleInitial:      ptrString(p, "middle_initial"),
			NameSuffix:         ptrString(p, "name_suffix"),
			TitleRank:          ptrString(p, "title_rank"),
			AddressLine1:       reqString(p, "address_line1"),
			AddressLine2:       ptrString(p, "address_line2"),
			AddressLine3:       ptrString(p, "address_line3"),
			AddressLine4:       ptrString(p, "address_line4"),
			City:               reqString(p, "city"),
			StateCode:          reqString(p, "state_code"),
			ZipCode:            reqString(p, "zip_code"),
			Email:              reqString(p, "email"),
			EmailSecondary:     ptrString(p, "email_secondary"),
			CommercialPhone:    reqString(p, "commercial_phone"),
			CommercialPhoneExt: ptrString(p, "commercial_phone_ext"),
			DsnPhone:           ptrString(p, "dsn_phone"),
			DsnPhoneExt:        ptrString(p, "dsn_phone_ext"),
			Fax:                ptrString(p, "fax"),
			Tld:                ptrString(p, "tld"),
			UserComments:       ptrString(p, "user_comments"),
		})
	case "host":
		return q.CreateNicRegHost(ctx, dbq.CreateNicRegHostParams{
			RegistrationID:     regID,
			Agency:             ptrString(p, "agency"),
			OrgHandle:          reqString(p, "org_handle"),
			PrimaryPocHandle:   reqString(p, "primary_poc_handle"),
			SecondaryPocHandle: reqString(p, "secondary_poc_handle"),
			Hostname:           reqString(p, "hostname"),
			RoleMailbox:        ptrString(p, "role_mailbox"),
			IpAddresses:        stringSlice(p, "ip_addresses"),
			UserComments:       ptrString(p, "user_comments"),
		})
	case "domain":
		return q.CreateNicRegDomain(ctx, dbq.CreateNicRegDomainParams{
			RegistrationID:        regID,
			Agency:                ptrString(p, "agency"),
			OrgHandle:             reqString(p, "org_handle"),
			TechPocHandle:         reqString(p, "tech_poc_handle"),
			AdminPocHandle:        reqString(p, "admin_poc_handle"),
			ZoneAdmin1:            ptrString(p, "zone_admin1"),
			ZoneAdmin2:            ptrString(p, "zone_admin2"),
			DomainName:            reqString(p, "domain_name"),
			RoleMailbox:           ptrString(p, "role_mailbox"),
			DnsServerHostnames:    stringSlice(p, "dns_server_hostnames"),
			MxServerHostname:      ptrString(p, "mx_server_hostname"),
			ReqCharter:            boolVal(p, "req_charter"),
			ReqFirewalled:         boolVal(p, "req_firewalled"),
			ReqNoSourceRoute:      boolVal(p, "req_no_source_route"),
			ReqDnsExclusive:       boolVal(p, "req_dns_exclusive"),
			ReqUps:                boolVal(p, "req_ups"),
			ReqSubordinateProtect: boolVal(p, "req_subordinate_protect"),
			ReqDiversePaths:       boolVal(p, "req_diverse_paths"),
			ReqWhoisRegistered:    boolVal(p, "req_whois_registered"),
			Justification:         ptrString(p, "justification"),
			UserComments:          ptrString(p, "user_comments"),
		})
	case "network":
		return q.CreateNicRegNetwork(ctx, dbq.CreateNicRegNetworkParams{
			RegistrationID:       regID,
			Agency:               ptrString(p, "agency"),
			OrgHandle:            reqString(p, "org_handle"),
			TechPocHandle:        reqString(p, "tech_poc_handle"),
			AdminPocHandle:       reqString(p, "admin_poc_handle"),
			ZoneAdmin:            ptrString(p, "zone_admin"),
			IpVersion:            reqString(p, "ip_version"),
			NetworkAggregator:    reqString(p, "network_aggregator"),
			Classification:       reqString(p, "classification"),
			CustomerNetworkName:  reqString(p, "customer_network_name"),
			TacticalNetwork:      ptrString(p, "tactical_network"),
			Ccsd:                 ptrString(p, "ccsd"),
			NiprnetHubIdentifier: ptrString(p, "niprnet_hub_identifier"),
			CcsPlatform:          ptrString(p, "ccs_platform"),
			CcsProvider:          ptrString(p, "ccs_provider"),
			CcsRegion:            ptrString(p, "ccs_region"),
			NetworkNumber:        ptrString(p, "network_number"),
			Cidr:                 ptrInt16(p, "cidr"),
			HostsInitial:         ptrInt32(p, "hosts_initial"),
			Hosts6mo:             ptrInt32(p, "hosts_6mo"),
			HostsMax:             ptrInt32(p, "hosts_max"),
			DisnTransport:        ptrString(p, "disn_transport"),
			GeophysicalLocation:  ptrString(p, "geophysical_location"),
			Num48Requested:       ptrInt32(p, "num_48_requested"),
			InaddrHostname1:      ptrString(p, "inaddr_hostname1"),
			InaddrIp1:            ptrString(p, "inaddr_ip1"),
			InaddrHostname2:      ptrString(p, "inaddr_hostname2"),
			InaddrIp2:            ptrString(p, "inaddr_ip2"),
			Justification:        ptrString(p, "justification"),
			UserComments:         ptrString(p, "user_comments"),
		})
	case "asn":
		return q.CreateNicRegAsn(ctx, dbq.CreateNicRegAsnParams{
			RegistrationID:    regID,
			Agency:            ptrString(p, "agency"),
			OrgHandle:         reqString(p, "org_handle"),
			TechPocHandle:     reqString(p, "tech_poc_handle"),
			AdminPocHandle:    reqString(p, "admin_poc_handle"),
			AsNumber:          ptrInt64(p, "as_number"),
			NetworkAggregator: reqString(p, "network_aggregator"),
			Classification:    reqString(p, "classification"),
			CustomerAsnName:   reqString(p, "customer_asn_name"),
			Igp:               ptrString(p, "igp"),
			Egp:               ptrString(p, "egp"),
			SitePremiseRouter: ptrString(p, "site_premise_router"),
			HubRouter:         ptrString(p, "hub_router"),
			NumRouters:        ptrInt32(p, "num_routers"),
			RouterIps:         ptrString(p, "router_ips"),
			NumNetworks:       ptrInt32(p, "num_networks"),
			NetworkIps:        ptrString(p, "network_ips"),
			Justification:     reqString(p, "justification"),
			UserComments:      reqString(p, "user_comments"),
		})
	case "dnskey":
		// Dates are validated present + parseable by the handler before
		// reaching here; default to zero only if somehow absent.
		start, _ := dateVal(p, "start_date")
		end, _ := dateVal(p, "end_date")
		return q.CreateNicRegDnskey(ctx, dbq.CreateNicRegDnskeyParams{
			RegistrationID: regID,
			DomainHandle:   reqString(p, "domain_handle"),
			StartDate:      start.UTC().Truncate(24 * time.Hour),
			EndDate:        end.UTC().Truncate(24 * time.Hour),
			KskValue:       ptrString(p, "ksk_value"),
			UserComments:   ptrString(p, "user_comments"),
		})
	default:
		return nil, &ValidationError{Msg: "unknown template_type: " + templateType}
	}
}

// fetchDetail loads the typed detail row for a registration by its type.
func fetchDetail(ctx context.Context, q Querier, regID uuid.UUID, templateType string) (any, error) {
	switch templateType {
	case "organization":
		return q.GetNicRegOrganization(ctx, regID)
	case "user":
		return q.GetNicRegUser(ctx, regID)
	case "host":
		return q.GetNicRegHost(ctx, regID)
	case "domain":
		return q.GetNicRegDomain(ctx, regID)
	case "network":
		return q.GetNicRegNetwork(ctx, regID)
	case "asn":
		return q.GetNicRegAsn(ctx, regID)
	case "dnskey":
		return q.GetNicRegDnskey(ctx, regID)
	default:
		return nil, &ValidationError{Msg: "unknown template_type: " + templateType}
	}
}
