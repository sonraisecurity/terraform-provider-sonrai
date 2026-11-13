package client

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Transaction is one submission of staged control changes.
type Transaction struct {
	ID string `json:"transactionId"`
	// Status is reported by the platform. Intermediate values vary with what is
	// being deployed, so only the terminal ones are given meaning here.
	Status    string `json:"status"`
	StatusMsg string `json:"statusMsg"`
	RootScope string `json:"rootScope"`
	// TemplateLocation is set once the transaction has produced a
	// CloudFormation template to deploy. It stays empty when the submitted
	// changes do not alter the template.
	TemplateLocation string `json:"templateLocation"`
	// PresignedTemplateSource is read only so HasTemplate can notice a ready
	// template that has a signed URL before a location. It is a short-lived
	// bearer credential, so nothing surfaces it as an attribute.
	PresignedTemplateSource string `json:"presignedTemplateSource"`
}

// Terminal transaction statuses.
//
// Only these are enumerated. The statuses a transaction passes through on the
// way depend on what is being deployed and are not a fixed set, so nothing
// branches on them: anything not listed here means "still running".
//
// The spelling and casing of a status are not relied on -- "Cancelled" and
// "Complete" are accepted as well as "CANCELLED" and "COMPLETED" -- so every
// comparison goes through NormalizeStatus rather than matching a literal. Getting this wrong makes a finished transaction look like a running
// one, and a wait then runs to its timeout.
const (
	StatusCompleted = "COMPLETED"
	StatusFailed    = "FAILED"
	StatusCancelled = "CANCELLED"
)

// NormalizeStatus folds the spellings of a transaction status onto the
// constants above. Unrecognised values are upper-cased and returned as-is,
// which keeps them non-terminal.
func NormalizeStatus(status string) string {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "CANCELLED", "CANCELED":
		return StatusCancelled
	case "COMPLETED", "COMPLETE":
		return StatusCompleted
	case "FAILED":
		return StatusFailed
	default:
		return strings.ToUpper(strings.TrimSpace(status))
	}
}

// IsTerminal reports whether the transaction has stopped moving.
func (t *Transaction) IsTerminal() bool {
	switch NormalizeStatus(t.Status) {
	case StatusCompleted, StatusFailed, StatusCancelled:
		return true
	default:
		return false
	}
}

// Failed reports whether the transaction ended badly.
func (t *Transaction) Failed() bool {
	switch NormalizeStatus(t.Status) {
	case StatusFailed, StatusCancelled:
		return true
	default:
		return false
	}
}

// HasTemplate reports whether a CloudFormation template is ready to deploy.
func (t *Transaction) HasTemplate() bool {
	return t.TemplateLocation != "" || t.PresignedTemplateSource != ""
}

const submitControlsMutation = `
mutation submitControls($rootScope: String!) {
  SubmitControls(input: {rootScope: {op: EQ, value: $rootScope}, onlyApplied: {op: EQ, value: false}}) {
    transactionId
  }
}`

const controlTransactionQuery = `
query controlTransaction($transactionId: String!) {
  ControlTransactions(where: {transactionId: {op: EQ, value: $transactionId}}) {
    items {
      transactionId
      status
      statusMsg
      rootScope
      templateLocation
      presignedTemplateSource
    }
  }
}`

// SubmitControls submits every change staged under rootScope as one transaction
// and returns its id.
//
// onlyApplied is fixed to false, the setting for a normal deploy. True regenerates the template from the entire applied control set,
// dragging a full rollout behind changes that cannot have altered it.
func (c *Client) SubmitControls(ctx context.Context, rootScope string) (string, error) {
	var out struct {
		SubmitControls struct {
			TransactionID string `json:"transactionId"`
		} `json:"SubmitControls"`
	}
	if err := c.ExecuteOnce(ctx, submitControlsMutation, map[string]any{"rootScope": rootScope}, &out); err != nil {
		return "", err
	}
	if out.SubmitControls.TransactionID == "" {
		return "", fmt.Errorf("SubmitControls returned no transaction id for %s", rootScope)
	}
	return out.SubmitControls.TransactionID, nil
}

// ControlTransaction reads one transaction by id.
func (c *Client) ControlTransaction(ctx context.Context, transactionID string) (*Transaction, error) {
	var out struct {
		ControlTransactions struct {
			Items []Transaction `json:"items"`
		} `json:"ControlTransactions"`
	}
	vars := map[string]any{"transactionId": transactionID}
	if err := c.Execute(ctx, controlTransactionQuery, vars, &out); err != nil {
		return nil, err
	}
	if len(out.ControlTransactions.Items) == 0 {
		return nil, fmt.Errorf("%w: transaction %q", ErrNotFound, transactionID)
	}
	transaction := out.ControlTransactions.Items[0]
	return &transaction, nil
}

// WaitForTransaction polls until the transaction either reaches a terminal
// status or produces a template to deploy, whichever comes first.
//
// Stopping at a ready template is deliberate. A transaction that needs one does
// not reach COMPLETED until the CloudFormation stack has been deployed, and this
// provider does not deploy it -- waiting for COMPLETED would block until
// something outside Terraform acted, or until the timeout.
//
// A failed or canceled transaction is returned with an error. The transaction
// is returned even then, so callers can record what the platform reported.
func (c *Client) WaitForTransaction(ctx context.Context, transactionID string, timeout time.Duration) (*Transaction, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()

	for {
		transaction, err := c.ControlTransaction(ctx, transactionID)
		if err != nil {
			return nil, err
		}

		switch {
		case transaction.Failed():
			return transaction, fmt.Errorf("transaction %s ended as %s: %s",
				transactionID, transaction.Status, transaction.StatusMsg)
		case transaction.IsTerminal(), transaction.HasTemplate():
			return transaction, nil
		}

		select {
		case <-ctx.Done():
			return transaction, ctx.Err()
		case <-deadline.C:
			return transaction, fmt.Errorf("timed out after %s waiting for transaction %s, last status %q",
				timeout, transactionID, transaction.Status)
		case <-ticker.C:
		}
	}
}

// WaitForTransactionStatus polls until the transaction reaches target.
//
// Only terminal statuses make sensible targets: a transient one can pass
// between two polls and never be observed. A transaction that settles on some
// other terminal status is an error, since it will never reach target.
func (c *Client) WaitForTransactionStatus(ctx context.Context, transactionID, target string, timeout time.Duration) (*Transaction, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()

	for {
		transaction, err := c.ControlTransaction(ctx, transactionID)
		if err != nil {
			return nil, err
		}

		if NormalizeStatus(transaction.Status) == NormalizeStatus(target) {
			return transaction, nil
		}
		if transaction.IsTerminal() {
			return transaction, fmt.Errorf("transaction %s settled on %s, which is terminal, while waiting for %s: %s",
				transactionID, transaction.Status, target, transaction.StatusMsg)
		}

		select {
		case <-ctx.Done():
			return transaction, ctx.Err()
		case <-deadline.C:
			return transaction, fmt.Errorf("timed out after %s waiting for transaction %s to reach %s, last status %q",
				timeout, transactionID, target, transaction.Status)
		case <-ticker.C:
		}
	}
}
