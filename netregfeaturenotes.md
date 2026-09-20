# NetReg Admin — Feature Notes (for reference when building similar app)

Source: UTK's NetReg Admin (network device registration / IPAM system), explored read-only.
This is a campus-wide network/IP asset management tool: device registration, DNS, DHCP,
subnet/IPAM, and a group-based permission system layered on Active Directory.

---

## 1. Global UI patterns (apply everywhere)

- **Persistent top nav** with 6 sections: Devices, Users, Groups, Domains, Subnets, Admin, Help —
  plus a logged-in user chip (name + netid) and logout link.
- **Global "CLI" command box** in the top-right of every page (a mini command palette):
  - Typing a recognized keyword + args jumps straight to a page/filtered view (e.g. `o kbirkela`
    → devices owned by that user, `log ofall` → activity log for that user, `r` → registration
    page, `rg` → resource groups admin).
  - Any unrecognized multi-character input is treated as a free-text device search (name/IP/MAC/label).
  - Every command has short aliases (`d`=devices, `u`=users, `g`=groups, `s`=subnets, `a`=admin,
    `l`=locations, `rg`=resource groups, `i`=impersonation, `w`=watch list, `log`, `c`=classify, `h`/`?`=help).
  - Contextual hint text throughout the app shows the CLI shortcut for the page you're on, e.g.
    "(o netid)" next to the Owner filter — teaches the CLI syntax in context.
- **DataTables-style grids everywhere** (devices, users' devices, groups, domains, subnets, locations,
  resource groups, roles, activity log):
  - Sortable columns, adjustable page size (10/20/50/100/...up to 1000+), pagination with ellipsis.
  - "Showing X to Y of Z entries" counter.
  - Export buttons: **Copy / Excel / CSV** on essentially every list.
  - Free-text "Search:" box on admin-style tables (client-side), plus dedicated structured filters
    on the bigger tables (Devices).
- **Collapsible/expandable panel pattern** (`▶ more filters`, `▶ Show/Hide Fields`, `▶ Bulk Edit`,
  `▶ Register Devices`) — keeps the default view simple but power features one click away.
- Color-coded status badges (e.g. green "Enabled" pill) and section header bars with icons
  (barcode icon = Device Identification, lock icon = Security Information, etc.) — consistent
  visual grouping of "categories of fields" across register/edit/view/bulk-edit forms.
- Sensitive live data (ARP cache entries, MAC status, VLAN, last-seen) shown redacted with
  a permission-gated tooltip ("Permissions Needed for Viewing ARP") rather than just hidden —
  i.e., users can see *that* data exists and who to ask, without leaking it.

## 2. Devices module

### List / search (Devices tab)
- Quick filter bar: Name/Address/MAC/Label (single combined field), Owner (netid), Primary User
  (netid), Resource Group.
- "More filters" expandable panel with 3 sub-groups:
  - **Security**: Device Ownership radio (UT-Owned / Not UT-Owned / All), Security Plan ID (GUID) search.
  - **Inventory**: Location (dropdown of ~200 campus buildings), Room number/name, Product, S/N,
    UT Tag Number, Operating System Type (free text + a reference list of standardized OS values
    shown as a scrollable list for consistency).
  - **Right column**: Attribute dropdown (All/Disabled/Not Disabled/DHCP/DHCP Reserved/Static),
    a **List Search** textarea (paste many comma- or newline-separated identifiers to bulk-search,
    explicitly overrides the simple search box), Device Comment search, and an inline **Help**
    accordion documenting the search syntax:
    - bare alphanumeric = exact match
    - `*` = wildcard ("0 or more of any character")
    - CIDR input (e.g. `10.0.0.0/25`) triggers a network-overlap search on IP + text search on other fields
    - MAC in any common format triggers exact MAC match + text search on other fields
    - other fields: exact match by default, `*` anywhere switches to "contains"
    - all searches case-insensitive
- **Show/Hide Fields** panel: checkboxes grouped into Network Connectivity, Security Information,
  Device Identification, Change Management — lets a user customize which columns appear in the grid
  (persists selection, e.g. IP Name/IP Address/Type/MAC/Owner/Primary User/Resource Group checked by default).
- **Bulk Edit Selected Devices** panel (operates on checkbox-selected rows):
  - Every field defaults to `~` (meaning "no change") so you only touch what you want to change.
  - Same 3 categories: Device Identification (Location, Room, OS Type, Comment), Security Information
    (togglable via "Change Security Information" checkbox to avoid accidental edits) with Ownership
    (UT-Owned, Owner, Primary User, Resource Group) and Security Plan ID.
  - Extra bulk actions: "Refresh DNS/DHCP" checkbox, "Bulk Delete All Selected Devices" checkbox,
    single "Change All Selected Devices" submit button.
- **Register Devices** panel: two entry points — "Register a New Device" (single, full form) and
  "Bulk Register New Devices" (implied CSV/batch style, not opened in this pass).

### Register / Edit Device form
Organized into clearly headed sections (same shape for register and edit):
- **Device Identification**: Location (required only if UT-Owned) + Room, Device Label (defaults to
  FQDN if blank, with helper text), Device Comment, Operating System Type, Product, S/N, UT Tag Number.
- **Security Information**:
  - Ownership: UT-Owned yes/no, Owner's NetID, Primary User's NetID, Resource Groups (optional CSV
    list, with an "available resource groups based on your permissions" reference list of ~180 short
    codes shown inline for lookup).
  - Sharing: mutually-exclusive choice between "Users who can share" (up to 9 comma-separated netids)
    OR "a Location that shares this device" (dropdown) — explicit "choose one, not both" guidance.
  - UT-Owned Security Management: Security Plan ID field, explicitly disabled/cleared unless
    UT-Owned = Yes (with an inline explanation of that dependency).
- **Network Connectivity**:
  - Naming: Hostname + Domain Name dropdown (a curated list of allowed DNS zones for self-service).
  - Aliases: free text, one per line, format `[TYPE]: [NAME]` (A, CNAME, etc.).
  - Addressing: section icon is a pair of opposing arrows (↔) to visually tie "Addressing" to the
    "Network Connectivity" header's own icon — reinforces the section-icon convention. MAC Address
    field with an inline format example (`00:0b:f3:60:55:0c`). Address Type radio (DHCP / DHCP
    Reserved / Static) — DHCP is the default and hides/disables the manual-IP box below it
    ("For reserved and static IPs only" caption on that box).
    For reserved/static, **two mutually exclusive ways to get an IP**, boxed together:
    - Option 1: type a specific IP directly.
    - Option 2: pick a Subnet from a dropdown (human name + CIDR), which then reveals a second,
      dependent dropdown to pick a specific available address from that subnet's pool — i.e. a
      cascading "subnet → free address in that subnet" picker rather than making the user know
      free IPs off-hand.
  - "Override DNS Conflicts" checkbox (guarded/rare-use permission).
  - Link out to an "IP Manager Request Form" for cases the self-service UI can't handle.
- Primary submit: single "Register Device" button, bottom-right, in a sticky-looking full-width
  footer bar separate from the form sections — keeps the call-to-action visible after a long form.

### Device detail (view) page
- Header: FQDN + short name, big Enabled/Disabled status pill, aliases toggle, action bar
  (Edit / Refresh DHCP-DDNS / Unregister).
- **Addressing** card: table of Location→Name/IP/MAC across NetReg DB / DHCP Lease / ARP-NetDB rows
  (so you can compare "what we think it is" vs "what the network currently sees"), a running
  **Change Notes / audit trail** (timestamped, attributed to a netid, e.g. "Created"), and an
  expandable "Addressing Details" for more.
- **Activity** card: MAC-address-found status, DHCP/DNS/ARP status icons with plain-language state
  (e.g. "Entry Found"), a permission-gated ARP Cache sub-panel (Status, Last seen, Last Switch,
  Last Port, VLAN), expandable "Activity Details".
- **Inventory** card: ownership type (Personally-Owned/UT-Owned), Location, Room, O/S, Product, S/N,
  UT Tag #, Device Comment — with a barcode icon motif tying back to the Device Identification concept.
- **Security Plan** card: Security Plan ID + explanation of what it's for.
- **Management and Users** card: Owner block, then a table of every associated person (Owner, Primary
  User, Sharing Users) each with directory-sourced NetID/Name/Email/Phone/Affiliation/Title/Department/
  Left-UT-Date — i.e. it enriches bare netids with live directory lookups everywhere a person is shown.
- **Resource Groups Owning Device** section.
- Edit page mirrors the register form, pre-filled, same section layout/colors (a maroon/brown header
  bar distinguishes "editing" from the teal "viewing" header).

## 3. Users module
- Search by NetID.
- Profile view (defaults to the logged-in user if no search yet) shows: Name, NetID, title/role,
  department, phone (or "Phone Number Not Found"), email, **AD Group Associations** (which AD groups
  they belong to), **Resource Group Associations** (table: group name + description), and
  **"Devices Owned By User"** — the standard device grid (with the same Excel/CSV export), scoped to
  what the requester has permission to see, with a footnote clarifying that scoping.

## 4. Groups module
- Landing page: a single table titled "AD Groups + Resource Groups + Application Roles" — one row per
  AD Group, columns = RW Resource Groups (links), RO Resource Groups (links), Application Roles
  (comma list) — i.e. a matrix view of the entire permission-mapping system at a glance.
  Explanatory sentence at top defines the three concepts in one line each.
  "Register Existing AD Group" action to onboard a new AD group into NetReg.
- **Resource Group Details** page (drill-in from a resource-group link), very rich:
  - Header: friendly name + short code.
  - "Accessible Network Resources" broken into three sub-tables, each independently paginated/exportable:
    - **Domains**: which DNS domains this resource group can touch, and (for shared domains) the full
      list of other resource groups also mapped to that domain ("+more..." truncation for long lists).
    - **Subnets**: VRF, Name, Location, VLAN, Network (CIDR), other Resource Groups sharing that subnet.
    - **Devices**: the actual device grid (IP Name, IP, MAC, Owner, Primary User, Location, Resource
      Groups), same searchable/exportable grid component reused again.
  - **AD Group Associations**: which AD Group(s) map into this resource group.
  - **AD Group Members Seen in NetReg**: a live directory-backed roster of everyone in that AD group
    (NetID, Name, Email, Phone, Affiliation, Title, Department) — reuses the "enrich netid with
    directory info" pattern from the device detail page.

## 5. Domains module
- Table of DNS zones: Domain, Resource Groups allowed to use it, Open/Protected flags, and full SOA
  record fields (Time to Live, Serial, Refresh, Retry, Expiry, Minimum, Primary NS, Secondary NS).
- Explains policy in a header blurb: some domains are fully "Open" for self-service registration,
  some "Protected" from casual use, some restricted to specific resource-group-mapped AD users.
- Admins can Add/Edit/Delete domains inline; "Add New Domain" button; searchable/exportable.

## 6. Subnets module
- Big paginated table (thousands of rows) of every subnet: Name, Network (CIDR), Gateway, VRF
  (e.g. PUB/PRIV/MOD1/MOD2/SEC/PCI/HIPAA/PLACEHOLDER — clearly a network-segmentation taxonomy),
  VLAN id, "Protected" flag, Location + Location Code, and the Resource Groups permitted to register
  devices into that subnet.
- Naming convention pattern worth copying: parallel subnets per building for Public / Management
  (PRIV) / two "MOD" tiers / Security-devices / VOIP / PCI / HIPAA / "(Unreg)" pre-registration holding
  subnets — i.e. the data model anticipates multiple purpose-specific subnets per physical location.
- Admins can Add/Edit/Delete; "Add New Subnet" button; searchable/exportable.

### Subnet Range and Usage Details page (drill-in from a subnet row)
- Header: friendly subnet name + CIDR, with an inline edit (pencil) icon next to the title for
  quick access to editing the subnet itself.
- Left-hand key/value summary card (alternating highlighted label column) with the subnet's core
  IPAM facts: Subnet Mask (dotted + bit-length, e.g. "255.255.255.0 (24 bits)"), Base address, Gateway
  ("none" when unset), Top (broadcast/last address), **Size** (address count, e.g. 256 — has a small
  icon with a hover tooltip explicitly stating "Calculated based on <CIDR>", i.e. a derived/read-only
  field labeled as such rather than left ambiguous), VRF, VLAN, Protected flag, Location, and a
  **DHCP Option Data** block rendered as raw formatted JSON (array of `{data, name}` pairs — e.g.
  routers/domain-name/domain-search) so admins can see exactly what DHCP options the subnet hands out.
  Followed by DHCP lease-timing fields (Valid/Max Valid/Min Valid Lifetime, in seconds), PXE-boot
  fields (Next Server, Boot File Name), DDNS Send Updates (Yes/No), and the Resource Groups permitted
  to use this subnet (linked out to their resource-group detail pages).
- **Subnet Pool Details** panel: a table of configured DHCP pools within the subnet (Pool Name, Client
  Class, Start/End Address, Pool Size, Addresses Used, Edit/Delete) — i.e. subnets can be broken into
  sub-ranges/pools (e.g. for different client classes), each independently tracked for
  capacity/utilization ("Addresses Used" vs "Pool Size"). Exportable (Copy/Excel/CSV); "Add New Pool"
  button. Shows a permission-scoped empty state ("No subnet pools found based on current user
  permissions") rather than a blank table.
- **NetReg Static and Reserved Devices Within Subnet** panel: the standard device grid, pre-filtered
  to this subnet, with subnet-relevant columns (IP Address, Type [static/dhcp_reserved], IP Name, MAC,
  Owner, Primary User, Location, Location Code, Resource Groups, O/S, UT-Owned) — lets an admin see
  subnet utilization by actual registered devices, distinct from the pool-level utilization above.
  Same pagination/export affordances as every other grid in the app.
- Overall pattern worth copying: a subnet detail page that combines (1) static IPAM/DHCP configuration
  facts, (2) DHCP pool-level capacity, and (3) actual device-level occupancy, all on one page — three
  different "who's using this range" views for one subnet.

## 7. Admin module (hub page)
Three grouped sections plus a live status widget:
- **Maintenance**: Locations, NetReg Application Roles, Resource Groups, Impersonation.
- **Reporting Features**: Edit MAC Watch List Notifications (placeholder/future), Send COPVIO Email
  (placeholder/future — abuse/violation notification workflow), NetReg Activity Log.
- **Other NetReg Related Sites**: Manage My Devices (simplified self-service view for non-admins),
  Network Connect/Captive Portal (register the device you're currently on), Guest Registration.
- **NetReg Status** widget: global Open/Closed toggle for the whole registration system (maintenance
  mode switch), rendered as a big toggle with iconography.
- **Currently Detected Host** widget: shows the requester's own live network fingerprint — DHCP lease
  FQDN, REMOTE_ADDR, DHCP lease hwaddress, User-Agent — useful for self-service troubleshooting/support
  ("what does the network currently think I am").

### Locations admin
- Master data table: Communications Code, Registrar Code, Name, Address, Physical Plant ID, Edit/Delete.
  "Create a New Location." (~200 locations) — this is the source list that populates every Location
  dropdown throughout the app.

### Application Roles admin (RBAC)
- A true permission matrix: rows = named roles (e.g. "helpdesk", "LANMAN", "ITES", "radius",
  "override_dns"), columns split into two groups — **Device Management Permissions** (Read Any,
  Write Any, Write Most, AirGroup Location, Override DNS) and **Network Management Permissions**
  (Manage Zones, Manage Subnets, Network Diagnostic, Log, Superuser).
- Color-coded legend distinguishes device-mgmt vs network-mgmt vs "use with caution" permissions.
- "Create a New Application Role."
- Help page spells out the conceptual model clearly (see Groups help notes below) — worth mirroring
  as in-app documentation, not just tooltips.

### Resource Groups admin
- Master list: short code + human description (e.g. `ag` → "Group - Agriculture"), Edit/Delete,
  "Create a New Resource Group." ~180 groups.

### Activity Log
- Full audit log with filters: NetID, date range ("Starting at"/"Ending at"), plus a "more filters"
  expander (not opened, but implied additional columns: Path, View, View Method, Status, Method,
  Query Params, Data, Errors, Response time in ms) — i.e. it's effectively an API request log with
  per-call latency, keyed to the acting user.

## 8. Permission model (conceptual — from Help)

Worth replicating as a design pattern:
- **AD Groups** (who) ↔ **Resource Groups** (what network resources: domains/subnets/devices) via
  Read-Write or Read-Only **resource pathways**.
- **AD Groups** (who) ↔ **Application Roles** (app-wide permissions like Superuser, Manage Zones) via
  **role pathways**.
- Everything is many-to-many at every link (a user can be in many AD groups; a resource can be in many
  resource groups; a role can bundle many permissions). At login, the app aggregates all pathways into
  one effective permission set for that user.
- Two distinct permission "levels" are explicitly named and documented for users: **resource-level**
  (scoped RW/RO on a subset of resources) vs **application-level** (global, e.g. Superuser bypasses
  resource-group scoping entirely). Most users only need resource-level; app-level roles are reserved
  for teams like helpdesk/security/desktop-support.
- Tooltips throughout the UI on disabled controls explain *which* permission/role would unlock them.

## 9. Domain concepts worth reusing
- **Three-category attribute taxonomy** applied consistently everywhere (register, edit, bulk-edit,
  view, show/hide-fields): Device/Network Identification, Security Information, Network Connectivity
  (+ a 4th "Change Management" bucket of Created/Modified timestamps in the column picker).
  Reusing the exact same 3–4 buckets across every device-related screen keeps the mental model consistent.
- **Required-field minimalism**: docs explicitly call out "only 9 of ~27 fields are required" and
  advertise a "Quick Register" path (`r` in the CLI, fill only the big-label fields, save) — good UX
  pattern of clearly marking optional vs. required among many fields rather than shrinking the form.
- **"~" sentinel for "no change" in bulk edit** — simple, clear convention for partial batch updates.
- **Directory enrichment**: anywhere a bare netid appears (owner, primary user, sharing user, AD group
  member), the UI resolves and displays live directory info (name/email/phone/affiliation/title/dept),
  reducing the need to cross-reference a separate directory tool.
- **Redact-but-disclose** pattern for sensitive live data (ARP/location tracking): show that the data
  exists and why it's hidden ("Permissions Needed for Viewing ARP") instead of omitting the section.
- **Self-service vs. admin split**: same underlying data, two front doors — "Manage My Devices" (simple)
  vs. full Devices tab (power-user filters/bulk tools) — plus a Captive-Portal-style flow for "register
  the device I'm on right now" and a separate Guest Registration flow.
- **Everything exportable**: Copy/Excel/CSV buttons are a near-universal affordance on every table,
  not just the main device grid.
