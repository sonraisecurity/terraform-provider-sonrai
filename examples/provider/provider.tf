terraform {
  required_providers {
    sonrai = {
      source  = "sonraisecurity/sonrai"
      version = "~> 0.1"
    }
  }
}

# Configured from the environment:
#
#   export SONRAI_ORG=crc12345678   # the first label of your console hostname
#   export SONRAI_TOKEN='...'       # issued to an App identity
#
# An argument set in this block takes precedence over its environment variable,
# so leave org out if you export SONRAI_ORG. Never set token here: it would be
# written into both configuration and state.
provider "sonrai" {}
