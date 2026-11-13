# One submission per root scope. Reference the controls it covers in triggers so
# that changing any of them produces a new submission.
resource "sonrai_control_submission" "firewall" {
  root_scope = "aws/r-abcd"

  triggers = {
    sagemaker = sonrai_account_disabled_service.sagemaker_prod.id
    textract  = sonrai_account_disabled_service.textract_prod.id
  }
}

output "transaction" {
  value = {
    id     = sonrai_control_submission.firewall.transaction_id
    status = sonrai_control_submission.firewall.status
  }
}
