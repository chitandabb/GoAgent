package evaluationledger

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

const InventorySchemaVersion = "evaluation_inventory_v1"

type Inventory struct {
	SchemaVersion string  `json:"schemaVersion"`
	ArtifactRoot  string  `json:"artifactRoot"`
	Assets        []Asset `json:"assets"`
}

func ParseInventory(reader io.Reader) (Inventory, error) {
	if reader == nil {
		return Inventory{}, errors.New("inventory reader is required")
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var inventory Inventory
	if err := decoder.Decode(&inventory); err != nil {
		return Inventory{}, fmt.Errorf("decode inventory: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return Inventory{}, fmt.Errorf("decode inventory: %w", err)
	}
	if err := inventory.Validate(); err != nil {
		return Inventory{}, err
	}
	return inventory, nil
}

func (i Inventory) Validate() error {
	if i.SchemaVersion != InventorySchemaVersion {
		return fmt.Errorf("unsupported inventory schemaVersion %q", i.SchemaVersion)
	}
	if strings.TrimSpace(i.ArtifactRoot) == "" || filepath.IsAbs(i.ArtifactRoot) {
		return errors.New("inventory artifactRoot is required and must be relative")
	}
	if len(i.Assets) == 0 {
		return errors.New("inventory contains no assets")
	}
	seen := make(map[string]struct{}, len(i.Assets))
	for index, asset := range i.Assets {
		if err := asset.Validate(); err != nil {
			return fmt.Errorf("asset %d: %w", index, err)
		}
		if asset.ID != strings.TrimSpace(asset.ID) {
			return fmt.Errorf("asset %d id must be trimmed", index)
		}
		if _, exists := seen[asset.ID]; exists {
			return fmt.Errorf("duplicate asset id %q", asset.ID)
		}
		seen[asset.ID] = struct{}{}
	}
	return nil
}

func (i Inventory) Asset(id string) (Asset, error) {
	if strings.TrimSpace(id) == "" || id != strings.TrimSpace(id) {
		return Asset{}, errors.New("asset id is required and must be trimmed")
	}
	for _, asset := range i.Assets {
		if asset.ID == id {
			return asset, nil
		}
	}
	return Asset{}, fmt.Errorf("asset %q is not present in inventory", id)
}
