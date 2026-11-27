# What a submission under this root scope would deploy if it ran now, before
# this configuration stages anything of its own.
data "sonrai_pending_changes" "firewall" {
  root_scope = "aws/r-abcd"
}

output "already_pending" {
  value = [for c in data.sonrai_pending_changes.firewall.changes : c.name]
}

# Reported as a warning on every plan and apply, without blocking either. A
# precondition on the submission would block instead, but it would also block
# the apply that recovers from one that staged changes and then failed.
check "nothing_unexpected_is_pending" {
  assert {
    condition     = data.sonrai_pending_changes.firewall.total == 0
    error_message = "${data.sonrai_pending_changes.firewall.total} change(s) are already pending and will be deployed by the next submission."
  }
}
