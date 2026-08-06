package webresearch

import (
	"strings"

	"github.com/chitandabb/GoAgent/internal/externalcase"
)

// SensitiveTermsFromExternalCase returns private task identifiers that must
// not be copied into a public provider query. Product/module terminology is
// intentionally left to the administrator dictionary because it may be public.
func SensitiveTermsFromExternalCase(item externalcase.ExternalCase) []string {
	values := []string{
		item.ExternalCaseKey,
		item.Customer.Code, item.Customer.Name,
		item.Production.WorkOrderNo, item.Production.WorkpieceNo,
		item.Production.MaterialCode, item.Production.BatchNo, item.Production.SerialNo,
		item.Production.FactoryCode, item.Production.WorkshopCode,
		item.Production.ProductionLineCode, item.Production.WorkstationCode,
		item.Production.EquipmentCode, item.Environment.BusinessDatabaseAlias,
	}
	for _, attachment := range item.Attachments {
		values = append(values, attachment.ExternalAttachmentKey, attachment.FileName)
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if len([]rune(trimmed)) < 2 || len([]rune(trimmed)) > maxSensitiveTermRunes {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, trimmed)
	}
	return result
}

func (p *QueryPolicy) SanitizeForExternalCase(input string, item externalcase.ExternalCase) (PublicQuery, error) {
	return p.Sanitize(input, SensitiveTermsFromExternalCase(item))
}
