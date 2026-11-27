# Every custom control in the organization, including ones made in the console.
data "sonrai_custom_controls" "all" {
  root_scope = "aws/r-abcd"
}

output "custom_controls" {
  value = {
    for c in data.sonrai_custom_controls.all.controls : c.control_key => {
      title       = c.title
      permissions = c.permissions
    }
  }
}

# Controls still deployed somewhere under the root scope. One of these cannot
# be destroyed until it has been detached from the scopes listed and that
# change deployed.
output "custom_controls_in_use" {
  value = {
    for c in data.sonrai_custom_controls.all.controls : c.control_key => c.deployed_scopes
    if length(c.deployed_scopes) > 0
  }
}
