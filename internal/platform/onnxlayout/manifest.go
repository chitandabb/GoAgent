package onnxlayout

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/chitandabb/GoAgent/internal/knowledgelayout"
)

const maxManifestBytes = 1024 * 1024

type Manifest struct {
	SchemaVersion int    `json:"schemaVersion"`
	Name          string `json:"name"`
	Provider      string `json:"provider"`
	License       string `json:"license"`
	Source        struct {
		Repository string `json:"repository"`
		Revision   string `json:"revision"`
		BaseURL    string `json:"baseUrl"`
		Files      []struct {
			Name   string `json:"name"`
			Bytes  int64  `json:"bytes"`
			SHA256 string `json:"sha256"`
		} `json:"files"`
	} `json:"source"`
	Conversion struct {
		Python          string `json:"python"`
		Paddle2ONNX     string `json:"paddle2onnx"`
		PaddlePaddle    string `json:"paddlepaddle"`
		ONNX            string `json:"onnx"`
		Opset           int    `json:"opset"`
		AutoUpdateOpset bool   `json:"autoUpdateOpset"`
		Checker         bool   `json:"checker"`
		OutputFile      string `json:"outputFile"`
		Bytes           int64  `json:"bytes"`
		SHA256          string `json:"sha256"`
	} `json:"conversion"`
	Preprocess struct {
		Version         string    `json:"version"`
		InputWidth      int       `json:"inputWidth"`
		InputHeight     int       `json:"inputHeight"`
		KeepAspectRatio bool      `json:"keepAspectRatio"`
		ColorOrder      string    `json:"colorOrder"`
		TensorLayout    string    `json:"tensorLayout"`
		Scale           float64   `json:"scale"`
		Mean            []float64 `json:"mean"`
		Std             []float64 `json:"std"`
	} `json:"preprocess"`
	Postprocess struct {
		Version        string  `json:"version"`
		ScoreThreshold float64 `json:"scoreThreshold"`
		NMSThreshold   float64 `json:"nmsThreshold"`
		NMSTopK        int     `json:"nmsTopK"`
		KeepTopK       int     `json:"keepTopK"`
	} `json:"postprocess"`
	Labels        []string            `json:"labels"`
	DomainMapping map[string][]string `json:"domainMapping"`

	regionTypes []knowledgelayout.RegionType
}

func LoadManifest(path string) (Manifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("open layout model manifest: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Manifest{}, fmt.Errorf("stat layout model manifest: %w", err)
	}
	if info.Size() < 1 || info.Size() > maxManifestBytes {
		return Manifest{}, errors.New("layout model manifest size is invalid")
	}

	decoder := json.NewDecoder(io.LimitReader(file, maxManifestBytes+1))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode layout model manifest: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Manifest{}, err
	}
	if err := manifest.validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("layout model manifest contains trailing JSON")
		}
		return fmt.Errorf("decode layout model manifest trailer: %w", err)
	}
	return nil
}

func (m *Manifest) validate() error {
	if m.SchemaVersion != 1 || strings.TrimSpace(m.Name) == "" ||
		strings.TrimSpace(m.Source.Revision) == "" || m.Conversion.Opset != 17 ||
		m.Conversion.AutoUpdateOpset || !m.Conversion.Checker || m.Conversion.Bytes < 1 ||
		m.Preprocess.InputWidth < 1 || m.Preprocess.InputHeight < 1 ||
		m.Preprocess.KeepAspectRatio || m.Preprocess.ColorOrder != "RGB" ||
		m.Preprocess.TensorLayout != "NCHW" || m.Preprocess.Scale <= 0 ||
		len(m.Preprocess.Mean) != 3 || len(m.Preprocess.Std) != 3 ||
		len(m.Labels) == 0 || len(m.Labels) > 10_000 ||
		m.Postprocess.ScoreThreshold <= 0 || m.Postprocess.ScoreThreshold > 1 ||
		m.Postprocess.KeepTopK < 1 {
		return errors.New("layout model manifest contract is invalid")
	}
	if len(m.Conversion.SHA256) != sha256HexLength || strings.ToLower(m.Conversion.SHA256) != m.Conversion.SHA256 {
		return errors.New("layout model manifest ONNX SHA-256 is invalid")
	}
	decodedSHA, err := hex.DecodeString(m.Conversion.SHA256)
	if err != nil || len(decodedSHA) != sha256HexLength/2 {
		return errors.New("layout model manifest ONNX SHA-256 is invalid")
	}
	for _, value := range m.Preprocess.Std {
		if value <= 0 {
			return errors.New("layout model manifest standard deviation is invalid")
		}
	}

	labelIndexes := make(map[string]int, len(m.Labels))
	for index, label := range m.Labels {
		if strings.TrimSpace(label) == "" {
			return errors.New("layout model manifest contains an empty label")
		}
		if _, exists := labelIndexes[label]; exists {
			return fmt.Errorf("layout model manifest contains duplicate label %q", label)
		}
		labelIndexes[label] = index
	}

	regionTypes := make([]knowledgelayout.RegionType, len(m.Labels))
	mapped := make([]bool, len(m.Labels))
	for key, labels := range m.DomainMapping {
		regionType := knowledgelayout.RegionType(key)
		if !regionType.Valid() || len(labels) == 0 {
			return fmt.Errorf("layout model manifest domain mapping %q is invalid", key)
		}
		for _, label := range labels {
			index, exists := labelIndexes[label]
			if !exists || mapped[index] {
				return fmt.Errorf("layout model manifest label mapping %q is invalid", label)
			}
			mapped[index] = true
			regionTypes[index] = regionType
		}
	}
	for index, isMapped := range mapped {
		if !isMapped {
			return fmt.Errorf("layout model manifest label %q is not mapped", m.Labels[index])
		}
	}
	m.regionTypes = regionTypes
	return nil
}

func (m Manifest) regionType(classID int) (knowledgelayout.RegionType, error) {
	if classID < 0 || classID >= len(m.regionTypes) {
		return "", fmt.Errorf("layout model class id %d is out of range", classID)
	}
	return m.regionTypes[classID], nil
}
