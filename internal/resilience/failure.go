package resilience

import "errors"

type FailureDisposition string

const (
	FailureStrict    FailureDisposition = "strict"
	FailureRetryable FailureDisposition = "retryable"
	FailureRejected  FailureDisposition = "rejected"
)

type classifiedFailure struct {
	disposition FailureDisposition
	err         error
}

func (e *classifiedFailure) Error() string { return e.err.Error() }
func (e *classifiedFailure) Unwrap() error { return e.err }

func StrictFailure(err error) error {
	return classifyFailure(err, FailureStrict)
}

func RetryableFailure(err error) error {
	return classifyFailure(err, FailureRetryable)
}

func classifyFailure(err error, disposition FailureDisposition) error {
	if err == nil {
		return nil
	}
	return &classifiedFailure{disposition: disposition, err: err}
}

func FailureDispositionOf(err error) FailureDisposition {
	var classified *classifiedFailure
	if errors.As(err, &classified) {
		return classified.disposition
	}
	return FailureRejected
}
