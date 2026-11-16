# Import ID: the transaction id. To adopt a deployment, import the transaction
# the stack is running, read from its SonraiId output ("<org>:<transaction id>").
# The imported submission takes on the configured triggers without submitting
# again.
import {
  to = sonrai_control_submission.firewall
  id = "a1b2c3d4-0000-4000-8000-000000000000"
}
