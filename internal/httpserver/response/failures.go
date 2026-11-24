package response

import (
	"errors"

	"piped/internal/credentials"
	"piped/internal/execution"
	"piped/internal/hosts"
	"piped/internal/jobs"
	"piped/internal/store"
)

func CredentialFailure(err error) error {
	switch {
	case errors.Is(err, credentials.ErrInput):
		return Fail(400, err.Error())
	case errors.Is(err, credentials.ErrBusy):
		return Fail(503, err.Error())
	case errors.Is(err, store.ErrCredentialInUse):
		return Fail(409, "credential is assigned to a host; reassign or remove the host first")
	case errors.Is(err, store.ErrCredentialName):
		return Fail(409, "credential name already exists")
	case errors.Is(err, store.ErrNotFound):
		return Fail(404, "credential not found")
	default:
		return err
	}
}

func HostFailure(err error) error {
	switch {
	case errors.Is(err, hosts.ErrInput), errors.Is(err, store.ErrHostPassword):
		return Fail(400, err.Error())
	case errors.Is(err, store.ErrHostName), errors.Is(err, store.ErrHostChanged), errors.Is(err, store.ErrHostInUse):
		return Fail(409, err.Error())
	case errors.Is(err, store.ErrHostCredential):
		return Fail(400, "SSH key credential not found")
	case errors.Is(err, store.ErrNotFound):
		return Fail(404, "host not found")
	default:
		return err
	}
}

func JobFailure(err error) error {
	switch {
	case errors.Is(err, jobs.ErrInput), errors.Is(err, store.ErrJobHost), errors.Is(err, store.ErrJobSecret):
		return Fail(400, err.Error())
	case errors.Is(err, store.ErrJobName), errors.Is(err, store.ErrJobChanged):
		return Fail(409, err.Error())
	case errors.Is(err, store.ErrNotFound):
		return Fail(404, "job not found")
	default:
		return err
	}
}

func ExecutionFailure(err error) error {
	switch {
	case errors.Is(err, execution.ErrInactive), errors.Is(err, execution.ErrApproval), errors.Is(err, store.ErrRunActive):
		return Fail(409, err.Error())
	case errors.Is(err, execution.ErrInput):
		return Fail(400, err.Error())
	case errors.Is(err, store.ErrNotFound):
		return Fail(404, "execution or job not found")
	default:
		return JobFailure(err)
	}
}
