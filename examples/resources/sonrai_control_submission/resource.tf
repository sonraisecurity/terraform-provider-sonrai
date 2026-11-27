# One submission per root scope. Reference the revision of each control it
# covers in triggers, so that anything one of them stages is submitted.
resource "sonrai_control_submission" "firewall" {
  root_scope = "aws/r-abcd"

  triggers = {
    sagemaker = sonrai_account_disabled_service.sagemaker_prod.revision
    textract  = sonrai_account_disabled_service.textract_prod.revision
  }
}

output "transaction" {
  value = {
    id     = sonrai_control_submission.firewall.transaction_id
    status = sonrai_control_submission.firewall.status
  }
}
