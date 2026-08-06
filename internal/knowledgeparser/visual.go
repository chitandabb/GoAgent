package knowledgeparser

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"path"
	"strings"
)

type VisualAssetKind string

const (
	VisualAssetEmbeddedImage VisualAssetKind = "embedded_image"
	VisualAssetSourceImage   VisualAssetKind = "source_image"
	VisualAssetDocumentPage  VisualAssetKind = "document_page"
	VisualAssetLayoutRegion  VisualAssetKind = "layout_region"
)

type VisualAsset struct {
	Index          int
	Kind           VisualAssetKind
	PageNumber     *int
	SectionPath    []string
	SourcePath     string
	SourcePart     string
	RelationshipID string
	MediaType      string
	SizeBytes      int64
	SHA256         string
	Width          int
	Height         int
	Content        []byte
}

func (a VisualAsset) Validate() error {
	if a.Index < 0 {
		return errors.New("knowledge visual asset index must not be negative")
	}
	switch a.Kind {
	case VisualAssetEmbeddedImage, VisualAssetSourceImage, VisualAssetDocumentPage, VisualAssetLayoutRegion:
	default:
		return errors.New("knowledge visual asset kind is invalid")
	}
	if a.PageNumber != nil && *a.PageNumber < 1 {
		return errors.New("knowledge visual asset page number must be positive")
	}
	if strings.TrimSpace(a.SourcePath) == "" || a.SourcePath != strings.TrimSpace(a.SourcePath) ||
		len([]rune(a.SourcePath)) > 1024 {
		return errors.New("knowledge visual asset source path is invalid")
	}
	if a.SourcePart != strings.TrimSpace(a.SourcePart) || len([]rune(a.SourcePart)) > 1024 {
		return errors.New("knowledge visual asset source part is invalid")
	}
	if a.RelationshipID != strings.TrimSpace(a.RelationshipID) || len([]rune(a.RelationshipID)) > 256 {
		return errors.New("knowledge visual asset relationship id is invalid")
	}
	if a.RelationshipID != "" && a.SourcePart == "" {
		return errors.New("knowledge visual asset relationship source part is required")
	}
	if !strings.HasPrefix(a.MediaType, "image/") &&
		!(a.Kind == VisualAssetDocumentPage && a.MediaType == "application/pdf") {
		return errors.New("knowledge visual asset media type is invalid")
	}
	if a.SizeBytes < 1 || !validVisualSHA256(a.SHA256) {
		return errors.New("knowledge visual asset size or sha256 is invalid")
	}
	if (a.Width == 0) != (a.Height == 0) || a.Width < 0 || a.Height < 0 {
		return errors.New("knowledge visual asset dimensions are invalid")
	}
	for _, value := range a.SectionPath {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return errors.New("knowledge visual asset section path is invalid")
		}
	}
	if a.Kind == VisualAssetDocumentPage {
		if len(a.Content) != 0 || a.PageNumber == nil {
			return errors.New("knowledge document page asset must reference source content by page")
		}
		return nil
	}
	if int64(len(a.Content)) != a.SizeBytes ||
		(a.Kind == VisualAssetSourceImage && (a.Width < 1 || a.Height < 1)) {
		return errors.New("knowledge raster visual asset content is invalid")
	}
	return nil
}

func newEmbeddedVisualAsset(index int, sourcePath string, content []byte) (VisualAsset, error) {
	asset, err := newRasterVisualAsset(
		index, VisualAssetEmbeddedImage, sourcePath, nil, nil, "", content,
	)
	if err == nil {
		return asset, nil
	}
	mediaType := officeVisualMediaType(sourcePath)
	if mediaType == "" {
		return VisualAsset{}, err
	}
	if mediaType == "image/png" || mediaType == "image/jpeg" || mediaType == "image/gif" {
		return VisualAsset{}, err
	}
	digest := sha256.Sum256(content)
	asset = VisualAsset{
		Index: index, Kind: VisualAssetEmbeddedImage, SourcePath: sourcePath,
		MediaType: mediaType, SizeBytes: int64(len(content)),
		SHA256: hex.EncodeToString(digest[:]), Content: append([]byte(nil), content...),
	}
	if err := asset.Validate(); err != nil {
		return VisualAsset{}, err
	}
	return asset, nil
}

func officeVisualMediaType(sourcePath string) string {
	switch strings.ToLower(path.Ext(sourcePath)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".bmp":
		return "image/bmp"
	case ".tif", ".tiff":
		return "image/tiff"
	case ".svg":
		return "image/svg+xml"
	case ".emf":
		return "image/emf"
	case ".wmf":
		return "image/wmf"
	default:
		return ""
	}
}

func newRasterVisualAsset(
	index int,
	kind VisualAssetKind,
	sourcePath string,
	pageNumber *int,
	sectionPath []string,
	relationshipID string,
	content []byte,
) (VisualAsset, error) {
	config, format, err := image.DecodeConfig(bytes.NewReader(content))
	if err != nil {
		return VisualAsset{}, fmt.Errorf("%w: decode visual asset %q: %v", ErrInvalidContent, sourcePath, err)
	}
	mediaType := ""
	switch strings.ToLower(format) {
	case "png":
		mediaType = "image/png"
	case "jpeg":
		mediaType = "image/jpeg"
	case "gif":
		mediaType = "image/gif"
	default:
		return VisualAsset{}, fmt.Errorf("%w: visual asset %q uses unsupported format %q", ErrInvalidContent, sourcePath, format)
	}
	digest := sha256.Sum256(content)
	asset := VisualAsset{
		Index: index, Kind: kind, PageNumber: clonePageNumber(pageNumber),
		SectionPath: append([]string(nil), sectionPath...), SourcePath: sourcePath,
		RelationshipID: strings.TrimSpace(relationshipID), MediaType: mediaType,
		SizeBytes: int64(len(content)), SHA256: hex.EncodeToString(digest[:]),
		Width: config.Width, Height: config.Height, Content: append([]byte(nil), content...),
	}
	if err := asset.Validate(); err != nil {
		return VisualAsset{}, err
	}
	return asset, nil
}

func newDocumentPageVisualAsset(index, pageNumber int, source []byte) (VisualAsset, error) {
	digest := sha256.Sum256(source)
	page := pageNumber
	asset := VisualAsset{
		Index: index, Kind: VisualAssetDocumentPage, PageNumber: &page,
		SourcePath: fmt.Sprintf("pages/%d", pageNumber), MediaType: "application/pdf",
		SizeBytes: int64(len(source)), SHA256: hex.EncodeToString(digest[:]),
	}
	if err := asset.Validate(); err != nil {
		return VisualAsset{}, err
	}
	return asset, nil
}

func NewLayoutRegionVisualAsset(index, pageNumber int, sourcePath string, content []byte) (VisualAsset, error) {
	if pageNumber < 1 {
		return VisualAsset{}, errors.New("knowledge layout region page number must be positive")
	}
	page := pageNumber
	return newRasterVisualAsset(
		index, VisualAssetLayoutRegion, sourcePath, &page, nil, "", content,
	)
}

func clonePageNumber(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func validVisualSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}
