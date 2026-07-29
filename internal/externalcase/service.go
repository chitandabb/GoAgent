package externalcase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/chitandabb/GoAgent/internal/apperror"
	"github.com/chitandabb/GoAgent/internal/repository"

	"github.com/google/uuid"
)

type Service struct {
	dataSource DataSource
	reader     Reader
	registry   Registry
	clock      func() time.Time
}

func NewService(dataSource DataSource, reader Reader, registry Registry) (*Service, error) {
	if dataSource.ID == uuid.Nil {
		return nil, errors.New("data source id is empty")
	}
	if reader == nil || registry == nil {
		return nil, errors.New("external case dependencies are nil")
	}
	return &Service{dataSource: dataSource, reader: reader, registry: registry, clock: time.Now}, nil
}

func (s *Service) DataSource() DataSource { return s.dataSource }

func (s *Service) List(ctx context.Context, query ListQuery) (ListResult, error) {
	if query.DataSourceID != s.dataSource.ID {
		return ListResult{}, apperror.New(apperror.CodeNotFound)
	}
	result, err := s.reader.List(ctx, query)
	if err != nil {
		return ListResult{}, translateSourceError("list external cases", err)
	}
	seen := make([]SeenCase, 0, len(result.Items))
	for _, item := range result.Items {
		seen = append(seen, SeenCase{ExternalCaseKey: item.ExternalCaseKey, ExternalCaseType: item.CaseType})
	}
	ids, err := s.registry.RegisterSeen(ctx, s.dataSource.ID, seen, s.clock().UTC())
	if err != nil {
		return ListResult{}, apperror.Wrap(apperror.CodeInternal, fmt.Errorf("register external cases: %w", err))
	}
	for i := range result.Items {
		result.Items[i].ID = ids[result.Items[i].ExternalCaseKey]
		result.Items[i].DataSourceID = s.dataSource.ID
		fingerprint, fingerprintErr := Fingerprint(result.Items[i])
		if fingerprintErr != nil {
			return ListResult{}, apperror.Wrap(apperror.CodeInternal, fmt.Errorf("fingerprint external case: %w", fingerprintErr))
		}
		result.Items[i].SourceFingerprint = fingerprint
	}
	return result, nil
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (*ExternalCase, error) {
	reference, err := s.registry.FindReference(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperror.New(apperror.CodeNotFound)
		}
		return nil, apperror.Wrap(apperror.CodeInternal, fmt.Errorf("find external case reference: %w", err))
	}
	if reference.DataSourceID != s.dataSource.ID {
		return nil, apperror.New(apperror.CodeNotFound)
	}
	item, err := s.reader.GetByKey(ctx, reference.ExternalCaseKey)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperror.New(apperror.CodeNotFound)
		}
		return nil, translateSourceError("get external case", err)
	}
	item.ID = reference.ID
	item.DataSourceID = reference.DataSourceID
	item.SourceFingerprint, err = Fingerprint(*item)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternal, fmt.Errorf("fingerprint external case: %w", err))
	}
	return item, nil
}

func translateSourceError(operation string, err error) error {
	if errors.Is(err, ErrUnavailable) {
		return apperror.Wrap(apperror.CodeDependencyUnavailable, fmt.Errorf("%s: %w", operation, err))
	}
	if errors.Is(err, ErrResultLimit) {
		return apperror.NewWithMessage(apperror.CodeValidationFailed, "查询结果超过安全限制，请缩小查询范围")
	}
	return apperror.Wrap(apperror.CodeInternal, fmt.Errorf("%s: %w", operation, err))
}

type unavailableReader struct{ cause error }

// NewUnavailableReader 在 SQL Server 启动连接失败时保留 API 路由，并让请求明确返回 503。
func NewUnavailableReader(cause error) Reader {
	return &unavailableReader{cause: cause}
}

func (r *unavailableReader) List(context.Context, ListQuery) (ListResult, error) {
	return ListResult{}, errors.Join(ErrUnavailable, r.cause)
}

func (r *unavailableReader) GetByKey(context.Context, string) (*ExternalCase, error) {
	return nil, errors.Join(ErrUnavailable, r.cause)
}
