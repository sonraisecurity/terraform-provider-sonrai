terraform {
  required_providers {
    sonrai = {
      source  = "sonraisecurity/sonrai"
      version = "~> 0.1"
    }
  }
}

# The token is read from SONRAI_TOKEN. Setting it here instead would write the
# credential into both configuration and state.
provider "sonrai" {
  org = "crc12345678"
}
