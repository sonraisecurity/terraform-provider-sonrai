# Deploy the template the submission produced.
resource "aws_cloudformation_stack" "firewall_controls" {
  name         = "SonraiCloudPermissionFirewall"
  template_url = sonrai_control_submission.firewall.template_location
  capabilities = ["CAPABILITY_NAMED_IAM"]
}

# Wait for the firewall to confirm the rollout landed.
data "sonrai_control_transaction" "firewall" {
  transaction_id = sonrai_control_submission.firewall.transaction_id
  wait_for       = "COMPLETED"

  depends_on = [aws_cloudformation_stack.firewall_controls]
}

output "firewall_rollout" {
  value = {
    transaction = data.sonrai_control_transaction.firewall.transaction_id
    status      = data.sonrai_control_transaction.firewall.status
    detail      = data.sonrai_control_transaction.firewall.status_message
  }
}
