package client

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSubmitControlsReturnsTransactionID(t *testing.T) {
	var got capture
	c := clientReturning(t, `{"data":{"SubmitControls":{"transactionId":"tx-123"}}}`, &got)

	id, err := c.SubmitControls(context.Background(), "aws/r-abcd")
	if err != nil {
		t.Fatalf("SubmitControls: %v", err)
	}
	if id != "tx-123" {
		t.Errorf("id = %q, want tx-123", id)
	}
	if got.Variables["rootScope"] != "aws/r-abcd" {
		t.Errorf("variables = %#v", got.Variables)
	}
	// onlyApplied true forces a full template rollout; it must stay out of the
	// request rather than become accidentally configurable.
	if !strings.Contains(got.Query, "onlyApplied: {op: EQ, value: false}") {
		t.Errorf("query should pin onlyApplied to false: %s", got.Query)
	}
}

func TestSubmitControlsFailsWithoutTransactionID(t *testing.T) {
	c := clientReturning(t, `{"data":{"SubmitControls":{"transactionId":""}}}`, nil)

	if _, err := c.SubmitControls(context.Background(), "aws/r-abcd"); err == nil {
		t.Fatal("expected an error when no transaction id comes back")
	}
}

func TestControlTransactionNotFound(t *testing.T) {
	c := clientReturning(t, `{"data":{"ControlTransactions":{"items":[]}}}`, nil)

	_, err := c.ControlTransaction(context.Background(), "tx-missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestTransactionPredicates(t *testing.T) {
	cases := []struct {
		name                          string
		tx                            Transaction
		terminal, failed, hasTemplate bool
	}{
		{"completed", Transaction{Status: StatusCompleted}, true, false, false},
		{"failed", Transaction{Status: StatusFailed}, true, true, false},
		{"cancelled", Transaction{Status: StatusCancelled}, true, true, false},
		// The platform has used both spellings; both must read as terminal.
		{"canceled single l", Transaction{Status: "CANCELED"}, true, true, false},
		{"lowercase", Transaction{Status: "cancelled"}, true, true, false},
		{"in progress", Transaction{Status: "SOME_INTERMEDIATE_STATUS"}, false, false, false},
		{"template ready", Transaction{Status: "SOME_INTERMEDIATE_STATUS", TemplateLocation: "https://s3/x.json"}, false, false, true},
		{"presigned only", Transaction{Status: "SOME_INTERMEDIATE_STATUS", PresignedTemplateSource: "https://s3/y"}, false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.tx.IsTerminal() != tc.terminal {
				t.Errorf("IsTerminal = %v, want %v", tc.tx.IsTerminal(), tc.terminal)
			}
			if tc.tx.Failed() != tc.failed {
				t.Errorf("Failed = %v, want %v", tc.tx.Failed(), tc.failed)
			}
			if tc.tx.HasTemplate() != tc.hasTemplate {
				t.Errorf("HasTemplate = %v, want %v", tc.tx.HasTemplate(), tc.hasTemplate)
			}
		})
	}
}

func TestWaitForTransactionStopsAtTemplate(t *testing.T) {

	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		// A transaction needing a template never reaches COMPLETED until the
		// stack is deployed, which this provider does not do -- so the wait has
		// to end when the template appears.
		if calls.Add(1) < 3 {
			_, _ = w.Write([]byte(`{"data":{"ControlTransactions":{"items":[{"transactionId":"tx-1","status":"SOME_INTERMEDIATE_STATUS"}]}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"ControlTransactions":{"items":[{"transactionId":"tx-1",
			"status":"SOME_INTERMEDIATE_STATUS","templateLocation":"https://s3/template.json"}]}}}`))
	})

	tx, err := c.WaitForTransaction(context.Background(), "tx-1", 5*time.Second)
	if err != nil {
		t.Fatalf("WaitForTransaction: %v", err)
	}
	if tx.TemplateLocation != "https://s3/template.json" {
		t.Errorf("template = %q", tx.TemplateLocation)
	}
}

func TestWaitForTransactionReturnsErrorOnFailure(t *testing.T) {
	c := clientReturning(t, `{"data":{"ControlTransactions":{"items":[{"transactionId":"tx-2",
		"status":"FAILED","statusMsg":"stack rollback"}]}}}`, nil)

	tx, err := c.WaitForTransaction(context.Background(), "tx-2", time.Second)
	if err == nil || !strings.Contains(err.Error(), "stack rollback") {
		t.Fatalf("error = %v, want the platform's status message", err)
	}
	// The transaction comes back even on failure so the caller can record it.
	if tx == nil || tx.Status != StatusFailed {
		t.Errorf("transaction = %#v, want the failed transaction", tx)
	}
}

func TestWaitForTransactionTimesOut(t *testing.T) {

	c := clientReturning(t, `{"data":{"ControlTransactions":{"items":[{"transactionId":"tx-3","status":"SOME_INTERMEDIATE_STATUS"}]}}}`, nil)

	_, err := c.WaitForTransaction(context.Background(), "tx-3", 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %v, want a timeout", err)
	}
}

func TestWaitForTransactionStatusReachesTarget(t *testing.T) {

	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			_, _ = w.Write([]byte(`{"data":{"ControlTransactions":{"items":[{"transactionId":"tx-4","status":"SOME_INTERMEDIATE_STATUS"}]}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"ControlTransactions":{"items":[{"transactionId":"tx-4","status":"COMPLETED"}]}}}`))
	})

	tx, err := c.WaitForTransactionStatus(context.Background(), "tx-4", StatusCompleted, 5*time.Second)
	if err != nil {
		t.Fatalf("WaitForTransactionStatus: %v", err)
	}
	if tx.Status != StatusCompleted {
		t.Errorf("status = %q, want %q", tx.Status, StatusCompleted)
	}
}

func TestWaitForTransactionStatusFailsOnOtherTerminalStatus(t *testing.T) {
	// A rolled-back CloudFormation deploy leaves the transaction FAILED. Waiting
	// for COMPLETED must fail the apply rather than spin until the timeout.
	c := clientReturning(t, `{"data":{"ControlTransactions":{"items":[{"transactionId":"tx-5",
		"status":"FAILED","statusMsg":"stack rollback"}]}}}`, nil)

	_, err := c.WaitForTransactionStatus(context.Background(), "tx-5", StatusCompleted, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "stack rollback") {
		t.Fatalf("error = %v, want the platform's status message", err)
	}
}

func TestWaitForTransactionStatusTimesOut(t *testing.T) {

	c := clientReturning(t, `{"data":{"ControlTransactions":{"items":[{"transactionId":"tx-6","status":"SOME_INTERMEDIATE_STATUS"}]}}}`, nil)

	_, err := c.WaitForTransactionStatus(context.Background(), "tx-6", StatusCompleted, 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %v, want a timeout", err)
	}
}

func TestNormalizeStatus(t *testing.T) {
	// A finished transaction that is not recognised as terminal makes a wait
	// run to its timeout, so both spellings and either case must fold.
	cases := map[string]string{
		"CANCELLED": StatusCancelled,
		"CANCELED":  StatusCancelled,
		"cancelled": StatusCancelled,
		"Cancelled": StatusCancelled,
		"COMPLETED": StatusCompleted,
		"COMPLETE":  StatusCompleted,
		"completed": StatusCompleted,
		"FAILED":    StatusFailed,
		"failed":    StatusFailed,
	}
	for in, want := range cases {
		if got := NormalizeStatus(in); got != want {
			t.Errorf("NormalizeStatus(%q) = %q, want %q", in, got, want)
		}
	}
	// Anything unrecognised stays non-terminal.
	if got := NormalizeStatus("SOME_INTERMEDIATE_STATUS"); got != "SOME_INTERMEDIATE_STATUS" {
		t.Errorf("unrecognised status = %q, should pass through", got)
	}
	tx := Transaction{Status: "SOME_INTERMEDIATE_STATUS"}
	if tx.IsTerminal() {
		t.Error("an unrecognised status must not read as terminal")
	}
}

func TestWaitForTransactionStatusAcceptsEitherSpelling(t *testing.T) {
	c := clientReturning(t, `{"data":{"ControlTransactions":{"items":[{"transactionId":"tx-7",
		"status":"CANCELLED","statusMsg":"cancelled"}]}}}`, nil)

	// Asking for the one-l spelling must still match the two-l response.
	tx, err := c.WaitForTransactionStatus(context.Background(), "tx-7", "CANCELED", time.Second)
	if err != nil {
		t.Fatalf("WaitForTransactionStatus: %v", err)
	}
	if tx.Status != "CANCELLED" {
		t.Errorf("status = %q, raw platform value should be preserved", tx.Status)
	}
}
