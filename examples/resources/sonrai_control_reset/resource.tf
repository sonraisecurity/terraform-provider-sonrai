# Arming this clears every control under the root scope, including controls
# this configuration did not create. Keep it behind a variable that defaults to
# false, and write confirm out by hand so one flipped flag is never enough.
variable "reset" {
  description = "Tear the deployment down and return every control to staged."
  type        = bool
  default     = false
}

# The reset transaction waits for the CloudFormation stack to be deleted before
# doing the rest of its work, so depends_on puts both into one apply: the stack
# goes, then the reset runs and this resource waits for it to complete.
resource "sonrai_control_reset" "firewall" {
  count = var.reset ? 1 : 0

  root_scope = "aws/r-abcd"
  confirm    = "aws/r-abcd"

  depends_on = [aws_cloudformation_stack.firewall]
}

output "reset" {
  value = var.reset ? {
    transaction_id = one(sonrai_control_reset.firewall[*].transaction_id)
    status         = one(sonrai_control_reset.firewall[*].status)
  } : null
}
