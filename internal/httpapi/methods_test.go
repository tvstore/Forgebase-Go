package httpapi

import (
	"fmt"
	"net/http"
	"testing"
)

// The mapping of the failure a method handler chose (M9.1, ADR 0019; the Rust impl From<MethodFailure> for
// ApiError). The calls themselves are tested through the public API, in methods_test.go of the root package.
func TestAMethodFailureKeepsItsClientStatusCodeAndMessage(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusConflict} {
		e := fromError(fmt.Errorf("wrapped: %w", &MethodFailure{Status: status, Code: "method_error", Message: "Pick another."}))
		if e.status != status || e.code != "method_error" || e.message != "Pick another." {
			t.Errorf("%d: %+v", status, e)
		}
	}
}

func TestAMethodFailureThatIsNotAClientErrorIsAGenericInternalError(t *testing.T) {
	for _, status := range []int{0, 200, 302, 500, 503} {
		e := fromError(&MethodFailure{Status: status, Code: "method_error", Message: "secret detail"})
		if e.status != http.StatusInternalServerError || e.code != "internal" || e.message != "internal server error" {
			t.Errorf("%d: %+v", status, e)
		}
	}
}
