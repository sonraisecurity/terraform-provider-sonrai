# A named group of permissions. It belongs to the whole organization and is
# defined as soon as this is applied, but protects nothing until it is attached.
resource "sonrai_custom_control" "data_destruction" {
  title       = "Data Destruction"
  cloud_type  = "aws"
  description = "Deleting buckets, tables and snapshots"

  # The complete list. Every permission is checked when planning.
  permissions = [
    "s3:DeleteBucket",
    "dynamodb:DeleteTable",
    "ec2:DeleteSnapshot",
  ]
}

# Attach it like a built-in service, by its control key. This part is staged,
# and takes effect when submitted and deployed.
resource "sonrai_account_protected_service" "data_destruction" {
  root_scope  = "aws/r-abcd"
  resource_id = "123456789012"
  control_key = sonrai_custom_control.data_destruction.control_key
}

resource "sonrai_control_submission" "firewall" {
  root_scope = "aws/r-abcd"

  triggers = {
    protect = sonrai_account_protected_service.data_destruction.revision
    # Moves when the permissions of the deployed control are edited, which is
    # what redeploys them. Without it the old list stays in force.
    control = sonrai_custom_control.data_destruction.revision
  }
}
