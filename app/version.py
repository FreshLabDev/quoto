"""Project identity: the version stamped into runtime logs and release checks,
plus the facts about who runs Quoto and under what licence.

They live together because two screens have to agree on them — the About card
and the signature under the user agreement — and the contact getting out of step
with reality is exactly how it ended up pointing at an account that never
existed.
"""

import os

# The running build. Dockerfile exports QUOTO_VERSION from the release
# workflow's tag, so a released image carries its own version and nothing here
# has to be edited for a release. A literal would go stale the moment it is not
# bumped and then name a release this is not -- which is exactly what v0.10.4
# shipped as, reporting 0.10.3 to its logs and to the About card.
VERSION = os.environ.get("QUOTO_VERSION", "").strip().lstrip("v") or "dev"

REPOSITORY = "FreshLabDev/quoto"
REPOSITORY_URL = f"https://github.com/{REPOSITORY}"
# GPL-3.0, not the Apache-2.0 the rest of the family uses. Read it off LICENSE
# rather than assuming: this line is shown in the About card and printed under
# the user agreement, so a wrong value is a false statement about licensing in
# two places people are meant to rely on.
LICENSE = "GPL-3.0"
OPERATOR = "Asterfield"
CONTACT = "@amtiyo"
CONTACT_URL = f"https://t.me/{CONTACT.lstrip('@')}"
