# Import ID: region_id@root_scope@resource_id.
#
# An imported control has no recorded previous state, so destroying it
# re-enables the region rather than restoring what was there before.
import {
  to = sonrai_region_control.no_apac_sandbox
  id = "ap-south-1@aws/r-abcd@123456789012"
}
