package postgres

import (
	"time"

	"gorm.io/gorm"
)

// incrementGlobalKnowledgeGeneration must be called inside the same
// transaction that changes the current retrievable Global knowledge set.
func incrementGlobalKnowledgeGeneration(tx *gorm.DB, changedAt time.Time) error {
	if tx == nil {
		return gorm.ErrInvalidDB
	}
	result := tx.Exec(`
UPDATE global_knowledge_generation
SET generation = generation + 1, updated_at = ?
WHERE singleton = 1`, changedAt.UTC())
	if result.Error != nil {
		return TranslateError(result.Error)
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
