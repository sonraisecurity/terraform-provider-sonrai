# Import ID: cloud_type:control_key. Find the key with the
# sonrai_custom_controls data source. The resource is authoritative for the
# permission list, so write all of them or the next apply removes the rest.
import {
  to = sonrai_custom_control.data_destruction
  id = "aws:datadestruction"
}
