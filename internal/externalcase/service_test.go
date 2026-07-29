package externalcase

import (
	"context"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/apperror"
	"github.com/chitandabb/GoAgent/internal/repository"

	"github.com/google/uuid"
)

func TestServiceListRegistersStableIdentityAndFingerprints(t *testing.T) {
	dataSourceID := uuid.New()
	caseID := uuid.New()
	reader := &readerStub{listResult: ListResult{Items: []ExternalCase{fingerprintFixture()}, Total: 1}}
	registry := &registryStub{ids: map[string]uuid.UUID{"TKT-1001": caseID}}
	service, err := NewService(DataSource{ID: dataSourceID}, reader, registry)
	if err != nil {
		t.Fatalf("service: %v", err)
	}

	result, err := service.List(context.Background(), ListQuery{DataSourceID: dataSourceID, Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if result.Items[0].ID != caseID || result.Items[0].DataSourceID != dataSourceID {
		t.Fatalf("identity was not assigned: %#v", result.Items[0])
	}
	if result.Items[0].SourceFingerprint == "" {
		t.Fatal("source fingerprint is empty")
	}
	if len(registry.seen) != 1 || registry.seen[0].ExternalCaseKey != "TKT-1001" {
		t.Fatalf("registered cases = %#v", registry.seen)
	}
}

func TestServiceRejectsUnconfiguredDataSource(t *testing.T) {
	service, _ := NewService(DataSource{ID: uuid.New()}, &readerStub{}, &registryStub{})
	_, err := service.List(context.Background(), ListQuery{DataSourceID: uuid.New()})
	if apperror.Normalize(err).Code != apperror.CodeNotFound {
		t.Fatalf("error = %v", err)
	}
}

func TestServiceTranslatesUnavailableSource(t *testing.T) {
	dataSourceID := uuid.New()
	service, _ := NewService(DataSource{ID: dataSourceID}, &readerStub{listErr: ErrUnavailable}, &registryStub{})
	_, err := service.List(context.Background(), ListQuery{DataSourceID: dataSourceID})
	if apperror.Normalize(err).Code != apperror.CodeDependencyUnavailable {
		t.Fatalf("error = %v", err)
	}
}

type readerStub struct {
	listResult ListResult
	listErr    error
	getResult  *ExternalCase
	getErr     error
}

func (s *readerStub) List(context.Context, ListQuery) (ListResult, error) {
	return s.listResult, s.listErr
}

func (s *readerStub) GetByKey(context.Context, string) (*ExternalCase, error) {
	return s.getResult, s.getErr
}

type registryStub struct {
	ids       map[string]uuid.UUID
	seen      []SeenCase
	reference *Reference
	err       error
}

func (s *registryStub) RegisterSeen(_ context.Context, _ uuid.UUID, seen []SeenCase, _ time.Time) (map[string]uuid.UUID, error) {
	s.seen = seen
	return s.ids, s.err
}

func (s *registryStub) FindReference(context.Context, uuid.UUID) (*Reference, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.reference == nil {
		return nil, repository.ErrNotFound
	}
	return s.reference, nil
}
