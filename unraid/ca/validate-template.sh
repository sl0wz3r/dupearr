#!/bin/sh
# validate-template.sh: check the Dupearr Unraid template (or ca_profile.xml) against the
# Community Applications template rules (summarised in unraid/ca/README.md).
#
#   sh unraid/ca/validate-template.sh [--private] [--icon unraid/icon.png] [--repo .] [--as unraid/dupearr.xml] unraid/dupearr.xml
#   sh unraid/ca/validate-template.sh --profile [--private] [--as ca_profile.xml] ca_profile.xml
#
#   --private  the LAN/Gitea variant (make ca-private): http:// URLs, private addresses and the
#              plain-HTTP registry note are allowed; every format rule still applies
#   --icon F   also check the local icon file the <Icon> URL serves: PNG, transparency, not
#              animated, square, big enough
#   --repo D   also check repository D the way CA clones it (git ls-files): one template per Name,
#              no deprecated/ folder, a LICENSE, ca_profile.xml at the root
#   --as P     the repository path the file is published at (default: the file's own path);
#              used when a freshly rendered file is checked before it is moved into place
#
# Prints PASS / WARN / FAIL / INFO per rule, then a summary. Exit 0 = no FAIL, 1 = at least one
# FAIL, 2 = usage error or xmllint missing. Needs xmllint (libxml2: macOS ships it; Debian and
# Ubuntu: apt install libxml2-utils) plus sed, grep, awk, od and tr.
set -eu

prog=validate-template.sh
usage() {
	sed -n '4,5p' "$0" | sed 's/^# *//' >&2
	exit 2
}

mode=template private=false icon='' repo='' as='' file=''
while [ $# -gt 0 ]; do
	case $1 in
	--profile) mode=profile; shift ;;
	--private) private=true; shift ;;
	--icon) [ $# -ge 2 ] || usage; icon=$2; shift 2 ;;
	--repo) [ $# -ge 2 ] || usage; repo=$2; shift 2 ;;
	--as) [ $# -ge 2 ] || usage; as=$2; shift 2 ;;
	-h | --help) usage ;;
	-*) usage ;;
	*) [ -z "$file" ] || usage; file=$1; shift ;;
	esac
done
[ -n "$file" ] || usage
[ -f "$file" ] || { printf '%s: %s not found\n' "$prog" "$file" >&2; exit 2; }
command -v xmllint >/dev/null 2>&1 || {
	printf '%s: xmllint is required (macOS: built in; Debian/Ubuntu: apt install libxml2-utils)\n' "$prog" >&2
	exit 2
}
[ -n "$as" ] || as=$file
as=${as#./}
# A path inside --repo counts as repository-relative.
if [ -n "$repo" ] && [ -d "$repo" ]; then
	repo_abs=$(cd "$repo" && pwd)
	case $as in /*) as_abs=$as ;; *) as_abs=$(pwd)/$as ;; esac
	case $as_abs in "$repo_abs"/*) as=${as_abs#"$repo_abs"/} ;; esac
fi

npass=0 nwarn=0 nfail=0
ok() { npass=$((npass + 1)); printf 'PASS  %s\n' "$*"; }
warn() { nwarn=$((nwarn + 1)); printf 'WARN  %s\n' "$*"; }
bad() { nfail=$((nfail + 1)); printf 'FAIL  %s\n' "$*"; }
info() { printf 'INFO  %s\n' "$*"; }
# x XPATH [FILE]: string/number result of an XPath expression ('' when nothing matches).
x() { xmllint --xpath "$1" "${2:-$file}" 2>/dev/null || :; }
# has TEXT ERE / hasi: does any line of TEXT match (case-insensitive for hasi)?
has() { printf '%s\n' "$1" | grep -Eq -- "$2"; }
hasi() { printf '%s\n' "$1" | grep -Eiq -- "$2"; }
count() { x "count($1)"; }
summary() {
	printf '\n%s: %s: %d passed, %d warnings, %d failed\n' "$prog" "$as" "$npass" "$nwarn" "$nfail"
	[ "$nfail" -eq 0 ]
}

# Private and loopback IPv4 ranges, plus LAN-only host names.
PRIVATE_RE='(^|[^0-9.])(10\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}|192\.168\.[0-9]{1,3}\.[0-9]{1,3}|172\.(1[6-9]|2[0-9]|3[01])\.[0-9]{1,3}\.[0-9]{1,3}|127\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}|169\.254\.[0-9]{1,3}\.[0-9]{1,3})([^0-9]|$)|localhost|\.(local|lan|home\.arpa|internal)([/:"<]|$)'
# Placeholders left by render.sh or copied from the CA starter repository.
PLACEHOLDER_RE='\{\{|\}\}|YOUR_[A-Z_]+|CHANGE_?ME|container_name|example-app|YOUR_SUPPORT_TOPIC'
REFERRAL_RE='[?&](ref|aff|affiliate|affid|tag|utm_[a-z]+)='
URL_ELEMENTS='Support Project ReadMe TemplateURL Icon Registry'

# ------------------------------------------------------------------------------ common checks
if ! err=$(xmllint --noout "$file" 2>&1); then
	bad "well-formed XML: $(printf '%s' "$err" | head -n 3 | tr '\n' ' ')"
	summary || exit 1
fi
ok "well-formed XML (xmllint)"

# A bare & breaks the template in CA; say where, even though xmllint would already have failed.
if lines=$(sed -E 's/&(amp|lt|gt|quot|apos|#[0-9]+|#x[0-9A-Fa-f]+);//g' "$file" | grep -n '&'); then
	bad "bare & (write &amp;) on line(s): $(printf '%s' "$lines" | cut -d: -f1 | tr '\n' ' ')"
else
	ok "no bare & characters"
fi

if lines=$(grep -nE -- "$PLACEHOLDER_RE" "$file"); then
	bad "placeholder text left (render.sh or CA starter values): line(s) $(printf '%s' "$lines" | cut -d: -f1 | tr '\n' ' ')"
else
	ok "no placeholders or starter values"
fi

nonascii=$(LC_ALL=C tr -d '\011\012\015\040-\176' <"$file" | wc -c | tr -d ' ')
if [ "$nonascii" -gt 0 ]; then
	warn "$nonascii non-ASCII byte(s): keep templates plain ASCII to avoid encoding surprises"
else
	ok "plain ASCII"
fi

if [ "$private" = true ]; then
	info "private (LAN) variant: http:// and private addresses allowed; never publish this file"
else
	if lines=$(grep -nEi -- "$PRIVATE_RE" "$file"); then
		bad "private address or LAN-only host name in a public file: $(printf '%s' "$lines" | head -n 3 | cut -c1-160 | tr '\n' ' ')"
	else
		ok "no private IP addresses or LAN host names"
	fi
fi

# ------------------------------------------------------------------------------ ca_profile.xml
if [ "$mode" = profile ]; then
	base=${as##*/}
	if [ "$base" = ca_profile.xml ]; then ok "file name is exactly ca_profile.xml"; else bad "file name must be exactly ca_profile.xml (is $base)"; fi
	case $as in
	*/*) bad "ca_profile.xml must be at the repository root (is $as)" ;;
	*) ok "ca_profile.xml is at the repository root ($as)" ;;
	esac
	root=$(x 'name(/*)')
	if [ "$root" = CommunityApplications ]; then ok "root element CommunityApplications"; else bad "root element must be CommunityApplications (is '$root')"; fi
	profile=$(x 'string(/CommunityApplications/Profile)')
	trimmed=$(printf '%s' "$profile" | tr -d ' \t\r\n')
	if [ -z "$trimmed" ]; then
		bad "<Profile> is empty: CA blocks the submission"
	elif [ "${#profile}" -lt 80 ]; then
		warn "<Profile> is very short (${#profile} characters): say what the repository holds and where to get support"
	else
		ok "<Profile> present (${#profile} characters)"
	fi
	if has "$profile" '<[A-Za-z/!?]'; then bad "<Profile> contains HTML; use Markdown"; else ok "<Profile> has no HTML"; fi
	if has "$profile" '^    '; then warn "<Profile> has a line indented by 4+ spaces (Markdown code block)"; fi
	for el in Icon WebPage Forum Discord Reddit Twitter Facebook DonateLink; do
		v=$(x "string(/CommunityApplications/$el)")
		n=$(count "/CommunityApplications/$el")
		if [ "$n" -eq 0 ]; then
			case $el in Icon | WebPage) warn "<$el> missing (recommended)" ;; Forum) info "<Forum> not set (add it once the support thread exists)" ;; esac
			continue
		fi
		if [ -z "$v" ]; then bad "<$el> is present but empty (remove it or fill it in)"; continue; fi
		if [ "$private" != true ] && ! has "$v" '^https://'; then bad "<$el> must be an https URL: $v"; continue; fi
		if hasi "$v" "$REFERRAL_RE"; then bad "<$el> looks like a referral/affiliate link: $v"; continue; fi
		if [ "$el" = Icon ] && hasi "$v" 'unraid-community-apps-starter'; then
			bad "<Icon> is the CA starter icon (the scan rejects it): $v"
			continue
		fi
		ok "<$el> $v"
	done
	summary || exit 1
	exit 0
fi

# ------------------------------------------------------------------------------ Docker template
root=$(x 'name(/*)')
ver=$(x 'string(/Container/@version)')
if [ "$root" = Container ] && [ "$ver" = 2 ]; then ok "root element <Container version=\"2\">"; else bad "root element must be <Container version=\"2\"> (is <$root version=\"$ver\">)"; fi

# One element each, where CA reads a single value.
for el in Name Repository Registry Network Shell Privileged Support Project ReadMe Overview Category WebUI TemplateURL Icon ExtraParams PostArgs Requires Beta MinVer MaxVer Date Changes License; do
	n=$(count "/Container/$el")
	[ "$n" -le 1 ] || bad "<$el> appears $n times (one only)"
done

name=$(x 'string(/Container/Name)')
if has "$name" '^[a-zA-Z0-9][a-zA-Z0-9_.-]+$'; then ok "<Name> '$name' is a valid container name"; else bad "<Name> '$name' is not a valid container name ([a-zA-Z0-9][a-zA-Z0-9_.-]+)"; fi

# ---- Repository (image)
image=$(x 'string(/Container/Repository)')
if [ -z "$image" ]; then
	bad "<Repository> is empty (minimum field)"
else
	imgname=${image%%@*}
	digest=''
	[ "$imgname" = "$image" ] || digest=${image#*@}
	path=${imgname%:*}
	tag=''
	case $path in */*) tag=${imgname##*:} ;; *) path=$imgname ;; esac
	[ "$path" != "$imgname" ] || tag=''
	case $path in */*) first=${path%%/*} ;; *) first='' ;; esac
	host=''
	case $first in *.* | *:* | localhost) host=$first ;; esac
	if has "$image" '[[:space:]]'; then bad "<Repository> contains whitespace: '$image'"; fi
	if has "$path" '[A-Z]'; then bad "<Repository> image name must be lower-case: $path"; else ok "<Repository> $image"; fi
	if [ "$private" != true ]; then
		case $host in
		'' | docker.io | index.docker.io | registry-1.docker.io | ghcr.io | lscr.io | quay.io | registry.gitlab.com | codeberg.org)
			ok "public registry (${host:-docker.io})" ;;
		*:* | localhost | [0-9]*.[0-9]*.[0-9]*.[0-9]*)
			bad "registry '$host' is an address or has a port: CA needs a publicly pullable image (ghcr.io or Docker Hub)" ;;
		*) warn "registry '$host' is not one CA commonly uses; make sure anyone can pull from it over HTTPS" ;;
		esac
		if [ -n "$digest" ]; then
			bad "<Repository> pins a digest: use :latest so Unraid's update check works (offer pinned tags with <Branch>)"
		elif [ -n "$tag" ] && [ "$tag" != latest ]; then
			bad "<Repository> uses tag ':$tag': use :latest so Unraid's update check follows releases"
		else
			ok "<Repository> follows :latest"
		fi
	fi
fi

# ---- URL elements
for el in $URL_ELEMENTS; do
	v=$(x "string(/Container/$el)")
	if [ -z "$v" ]; then
		case $el in
		Support | Project) : ;; # checked together below
		Registry | ReadMe) warn "<$el> missing or empty (recommended)" ;;
		*) bad "<$el> missing or empty (recommended by CA; required by this project)" ;;
		esac
		continue
	fi
	if ! has "$v" '^https?://[^[:space:]]+$'; then bad "<$el> is not a URL: $v"; continue; fi
	if [ "$private" != true ] && ! has "$v" '^https://'; then bad "<$el> must use https://: $v"; continue; fi
	if hasi "$v" "$REFERRAL_RE"; then bad "<$el> looks like a referral/affiliate link (disallowed): $v"; continue; fi
	ok "<$el> $v"
done
support=$(x 'string(/Container/Support)')
project=$(x 'string(/Container/Project)')
if [ -z "$support" ] && [ -z "$project" ]; then bad "neither <Support> nor <Project>: CA drops such templates automatically"; else ok "<Support> or <Project> present"; fi

# ---- TemplateURL must be the raw URL of this very file
turl=$(x 'string(/Container/TemplateURL)')
if [ -n "$turl" ]; then
	base=${as##*/}
	if [ "$private" != true ]; then
		if has "$turl" '^https://raw\.githubusercontent\.com/[^/]+/[^/]+/[^/]+/'; then
			ok "<TemplateURL> is a raw.githubusercontent.com URL"
			branch=$(printf '%s' "$turl" | sed -E 's#^https://raw\.githubusercontent\.com/[^/]+/[^/]+/([^/]+)/.*#\1#')
			case $branch in main | master) ok "<TemplateURL> reads the $branch branch" ;; *) warn "<TemplateURL> reads branch '$branch': CA reads the default branch (main)" ;; esac
		else
			bad "<TemplateURL> must be the raw.githubusercontent.com URL of this file: $turl"
		fi
	fi
	case $turl in
	*/"$as") ok "<TemplateURL> ends with this file's repository path ($as)" ;;
	*/"$base") warn "<TemplateURL> ends with $base but not with $as (fine only for a separate templates repository)" ;;
	*) bad "<TemplateURL> does not point at this file ($as): $turl" ;;
	esac
fi

# ---- the same owner everywhere (typo guard)
gh_owner() { printf '%s' "$1" | sed -nE 's#^https://(github\.com|raw\.githubusercontent\.com)/([^/]+)/.*#\2#p' | tr '[:upper:]' '[:lower:]'; }
if [ "$private" != true ]; then
	powner=$(gh_owner "$project")
	if [ -n "$powner" ]; then
		for el in Support ReadMe TemplateURL Icon Registry; do
			o=$(gh_owner "$(x "string(/Container/$el)")")
			[ -z "$o" ] || [ "$o" = "$powner" ] || warn "<$el> owner '$o' differs from <Project> owner '$powner'"
		done
		case $image in
		ghcr.io/*) iowner=$(printf '%s' "${image#ghcr.io/}" | cut -d/ -f1)
			if [ "$iowner" = "$powner" ]; then ok "image owner matches <Project> owner ($powner)"; else
				warn "image owner '$iowner' differs from <Project> owner '$powner'"; fi ;;
		esac
	fi
fi

# ---- Icon
iurl=$(x 'string(/Container/Icon)')
case $iurl in
'') : ;;
*.png | *.png\?*) ok "<Icon> is a PNG" ;;
*.svg | *.svg\?*) warn "<Icon> is an SVG: a transparent PNG is what CA recommends" ;;
*) bad "<Icon> should be a transparent PNG: $iurl" ;;
esac
if [ -n "$icon" ]; then
	if [ ! -f "$icon" ]; then
		bad "icon file not found: $icon"
	else
		sig=$(od -An -tx1 -N8 "$icon" | tr -d ' \n')
		if [ "$sig" != 89504e470d0a1a0a ]; then
			bad "$icon is not a PNG file"
		else
			# walk the PNG chunks: length(4) type(4) data crc(4)
			off=8 types='' w=0 h=0 ctype=0
			while :; do
				# shellcheck disable=SC2046 # split od's byte list into $1..$8 on purpose
				set -- $(od -An -tu1 -j"$off" -N8 "$icon")
				[ $# -eq 8 ] || break
				len=$((($1 << 24) | ($2 << 16) | ($3 << 8) | $4))
				# shellcheck disable=SC2059 # octal escapes built from the byte values
				t=$(printf "\\$(printf %03o "$5")\\$(printf %03o "$6")\\$(printf %03o "$7")\\$(printf %03o "$8")")
				types="$types $t"
				if [ "$t" = IHDR ]; then
					# shellcheck disable=SC2046
					set -- $(od -An -tu1 -j$((off + 8)) -N10 "$icon")
					w=$((($1 << 24) | ($2 << 16) | ($3 << 8) | $4))
					h=$((($5 << 24) | ($6 << 16) | ($7 << 8) | $8))
					ctype=${10}
				fi
				[ "$t" != IEND ] || break
				off=$((off + 12 + len))
			done
			bytes=$(wc -c <"$icon" | tr -d ' ')
			case " $types " in *" acTL "*) bad "$icon is an animated PNG (not allowed for app icons)" ;; *) ok "$icon is not animated" ;; esac
			case $ctype in
			4 | 6) ok "$icon has an alpha channel (transparent background possible)" ;;
			*) case " $types " in *" tRNS "*) ok "$icon has transparency (tRNS)" ;; *) bad "$icon has no transparency: use an RGBA PNG" ;; esac ;;
			esac
			if [ "$w" -ne "$h" ]; then warn "$icon is not square (${w}x${h})"; fi
			if [ "$w" -lt 64 ]; then bad "$icon is too small (${w}x${h})"; elif [ "$w" -lt 256 ]; then warn "$icon is small (${w}x${h}); CA asks for a high-resolution icon"; else ok "$icon is ${w}x${h}, $bytes bytes"; fi
			[ "$bytes" -le 1048576 ] || warn "$icon is larger than 1 MiB ($bytes bytes)"
			case $iurl in */"${icon##*/}" | */"${icon##*/}"\?*) ok "<Icon> URL serves ${icon##*/}" ;; '') : ;; *) warn "<Icon> URL does not end with ${icon##*/}: $iurl" ;; esac
		fi
	fi
fi

# ---- Overview
ov=$(x 'string(/Container/Overview)')
if [ -z "$(printf '%s' "$ov" | tr -d ' \t\r\n')" ]; then
	bad "<Overview> is empty: every app needs a reasonable description"
else
	if [ "${#ov}" -lt 150 ]; then warn "<Overview> is short (${#ov} characters)"; else ok "<Overview> present (${#ov} characters)"; fi
	if has "$ov" '<[A-Za-z/!?]'; then bad "<Overview> contains HTML (Unraid strips it; use plain text or Markdown)"; else ok "<Overview> has no HTML"; fi
	if has "$ov" '[][]'; then bad "<Overview> contains [ or ]: CA turns them into < > and strips them as tags (Markdown links do not work either)"; else ok "<Overview> has no square brackets"; fi
	if has "$ov" '^    '; then warn "<Overview> has a line indented by 4+ spaces (shown as non-breaking spaces)"; fi
	stars=$(printf '%s' "$ov" | tr -cd '*' | wc -c | tr -d ' ')
	[ $((stars % 2)) -eq 0 ] || warn "<Overview> has an odd number of * characters (unbalanced Markdown emphasis; write arr, not *arr)"
	# Dupearr policy: a deletion tool states the risk and its safe defaults up front
	# (CA's policy allows blacklisting an app over a bug that loses data).
	missing=''
	hasi "$ov" 'delet' || missing="$missing 'deletes'"
	hasi "$ov" 'dry run' || missing="$missing 'dry run'"
	hasi "$ov" 'approv' || missing="$missing 'approval'"
	if [ -n "$missing" ]; then bad "<Overview> must state that Dupearr deletes files and ships with dry run and manual approval on (missing:$missing)"; else ok "<Overview> states the deletion risk, dry run and approval defaults"; fi
fi

# ---- Category
cat=$(x 'string(/Container/Category)')
if [ -z "$cat" ]; then
	bad "<Category> missing"
else
	badtok=''
	for tok in $cat; do
		case $tok in
		AI: | Backup: | Cloud: | Crypto: | Downloaders: | Drivers: | GameServers: | HomeAutomation: | MediaApp: | MediaServer: | Network: | Plugins: | Productivity: | Security: | Tools: | Other:) ;;
		MediaApp:Books | MediaApp:Music | MediaApp:Photos | MediaApp:Video | MediaApp:Other) ;;
		MediaServer:Books | MediaServer:Music | MediaServer:Photos | MediaServer:Video | MediaServer:Other) ;;
		Network:DNS | Network:FTP | Network:Management | Network:Messenger | Network:Proxy | Network:Voip | Network:VPN | Network:Privacy | Network:Web | Network:Other) ;;
		Tools:System | Tools:Utilities) ;;
		*) badtok="$badtok $tok" ;;
		esac
	done
	if [ -n "$badtok" ]; then bad "<Category> has unknown token(s):$badtok"; else ok "<Category> '$cat' uses valid tokens"; fi
fi

# ---- WebUI and ports
webui=$(x 'string(/Container/WebUI)')
if [ -z "$webui" ]; then
	warn "<WebUI> missing (Dupearr has a web UI)"
elif has "$webui" '^https?://\[IP\]:\[PORT:[0-9]+\]'; then
	wport=$(printf '%s' "$webui" | sed -E 's/.*\[PORT:([0-9]+)\].*/\1/')
	if [ "$(count "/Container/Config[@Type='Port' and @Target='$wport']")" -ge 1 ]; then
		ok "<WebUI> $webui uses container port $wport, which is a Port entry"
	else
		bad "<WebUI> uses [PORT:$wport] but no Config Type=\"Port\" has Target=\"$wport\" (CA removes or rewrites such templates)"
	fi
else
	bad "<WebUI> must be http://[IP]:[PORT:container-port]/... with no hard-coded address or port: $webui"
fi

# ---- flags
priv=$(x 'string(/Container/Privileged)')
if [ "$priv" = false ]; then ok "<Privileged> false"; else bad "<Privileged> must be false (is '$priv')"; fi
if [ "$(count /Container/MaxVer)" -gt 0 ]; then bad "<MaxVer> is set: it hides the app on newer Unraid releases"; else ok "no <MaxVer>"; fi
minver=$(x 'string(/Container/MinVer)')
if [ -z "$minver" ] || has "$minver" '^[0-9]+(\.[0-9]+)*$'; then ok "<MinVer> '${minver:-none}'"; else warn "<MinVer> '$minver' is not a version number"; fi
net=$(x 'string(/Container/Network)')
case $net in bridge | host) ok "<Network> $net" ;; '') warn "<Network> missing (bridge)" ;; *) warn "<Network> '$net': bridge is the portable default" ;; esac
sh_=$(x 'string(/Container/Shell)')
case $sh_ in sh | bash | '') ok "<Shell> '${sh_:-default}'" ;; *) warn "<Shell> '$sh_' (sh or bash)" ;; esac
for el in Deprecated DeprecatedMaxVer; do
	[ "$(count "/Container/$el")" -eq 0 ] || bad "<$el> is set"
done

# ---- ExtraParams / PostArgs: flags only, never commands (instant blacklist otherwise)
ep=$(x 'string(/Container/ExtraParams)')
if [ -n "$ep" ]; then
	epok=true
	if has "$ep" '[;|&`<>\\]|\$\('; then bad "<ExtraParams> contains shell metacharacters: $ep"; epok=false; fi
	set -f
	for w in $ep; do
		case $w in -*) ;; *) bad "<ExtraParams> word '$w' is not a docker flag (no commands allowed)"; epok=false ;; esac
	done
	set +f
	if has "$ep" '(^|[[:space:]])--privileged'; then bad "<ExtraParams> uses --privileged"; epok=false; fi
	if has "$ep" '(^|[[:space:]])--restart'; then warn "<ExtraParams> sets --restart (Unraid manages restarts; CA rewrites it)"; fi
	if has "$ep" '--pids-limit='; then warn "<ExtraParams> --pids-limit=N is not recognised by Unraid (use the space form)"; fi
	[ "$epok" = false ] || ok "<ExtraParams> are docker flags only: $ep"
fi
pa=$(x 'string(/Container/PostArgs)')
[ -z "$pa" ] || warn "<PostArgs> is set ('$pa'); CA's field reference does not list it"

# ---- Requires
req=$(x 'string(/Container/Requires)')
if [ -n "$req" ]; then
	if has "$req" '<[A-Za-z/!?]'; then bad "<Requires> contains HTML"; fi
	if [ "$private" != true ] && hasi "$req$ov" 'insecure-registr|plain[- ]http'; then
		bad "<Requires>/<Overview> still mention the plain-HTTP registry or --insecure-registry"
	else
		ok "<Requires> present, no LAN registry instructions"
	fi
fi

# ---- Date, Changes, Beta
date=$(x 'string(/Container/Date)')
if [ -z "$date" ]; then warn "<Date> missing"; elif has "$date" '^[0-9]{4}-[0-9]{2}-[0-9]{2}( [0-9]{2}:[0-9]{2}:[0-9]{2})?$'; then ok "<Date> $date"; else bad "<Date> must be YYYY-MM-DD: $date"; fi
changes=$(x 'string(/Container/Changes)')
cver=''
if [ -z "$changes" ]; then
	warn "<Changes> missing (feeds the Updated Apps list)"
else
	if has "$changes" '[][]'; then bad "<Changes> contains [ or ]: CA turns them into tags and strips them"; else ok "<Changes> has no square brackets"; fi
	if has "$changes" '<[A-Za-z/!?]'; then bad "<Changes> contains HTML"; fi
	if hasi "$changes" 'unreleased'; then warn "<Changes> still says 'unreleased'"; fi
	cver=$(printf '%s\n' "$changes" | sed -nE 's/^#+ *v?([0-9]+\.[0-9]+\.[0-9]+[^ ]*).*/\1/p' | head -n 1)
	if [ -n "$cver" ]; then ok "<Changes> starts with version $cver"; else warn "<Changes> has no '### X.Y.Z' heading"; fi
	if [ -n "$date" ] && ! printf '%s\n' "$changes" | head -n 1 | grep -qF "$date"; then warn "<Changes> first heading does not mention <Date> $date"; fi
fi
beta=$(x 'string(/Container/Beta)')
case $cver in
0.*) if [ "$beta" = true ]; then ok "<Beta> true while the version is 0.x"; else bad "<Beta> must be true while the version is 0.x ($cver)"; fi ;;
*) [ "$beta" != true ] || [ -z "$cver" ] || warn "<Beta> is true for version $cver" ;;
esac

# ---- Config entries: one line each, dockerMan attribute values, no tag-like text
if lines=$(awk '/<Config[ >]/ { n = gsub(/<Config[ >]/, "&"); if (n > 1 || ($0 !~ /<\/Config>/ && $0 !~ /\/>[[:space:]]*$/)) print NR }' "$file"); [ -n "$lines" ]; then
	bad "Config element(s) not on a single line (dockerMan format): line(s) $(printf '%s' "$lines" | tr '\n' ' ')"
else
	ok "every <Config> is on one line"
fi
n=$(count /Container/Config)
names='' targets='' cfgok=true
i=1
while [ "$i" -le "$n" ]; do
	c="/Container/Config[$i]"
	cn=$(x "string($c/@Name)") ct=$(x "string($c/@Target)") ctype=$(x "string($c/@Type)")
	cdisp=$(x "string($c/@Display)") creq=$(x "string($c/@Required)") cmask=$(x "string($c/@Mask)")
	cmode=$(x "string($c/@Mode)") cdesc=$(x "string($c/@Description)") cdef=$(x "string($c/@Default)")
	cval=$(x "string($c)")
	label="Config '$cn'"
	[ -n "$cn" ] || { bad "Config #$i has no Name"; cfgok=false; }
	[ -n "$ct" ] || { bad "$label has no Target"; cfgok=false; }
	case $ctype in Port | Path | Variable | Label | Device) ;; *) bad "$label Type '$ctype' (Port, Path, Variable, Label or Device)"; cfgok=false ;; esac
	case $cdisp in always | advanced | always-hide | advanced-hide) ;; *) bad "$label Display '$cdisp'"; cfgok=false ;; esac
	case $creq in true | false) ;; *) bad "$label Required '$creq' (true or false)"; cfgok=false ;; esac
	case $cmask in true | false) ;; *) bad "$label Mask '$cmask' (true or false)"; cfgok=false ;; esac
	case $ctype in
	Port)
		has "$ct" '^[0-9]+$' || { bad "$label port Target '$ct' is not a number"; cfgok=false; }
		case $cmode in tcp | udp) ;; *) bad "$label port Mode '$cmode' (tcp or udp)"; cfgok=false ;; esac ;;
	Path)
		case $ct in /*) ;; *) bad "$label path Target '$ct' is not absolute"; cfgok=false ;; esac
		has "$cmode" '^(rw|ro)(,(slave|shared))?$' || { bad "$label path Mode '$cmode' (rw or ro)"; cfgok=false; } ;;
	esac
	for a in "$cn" "$cdesc" "$cdef" "$cval"; do
		if has "$a" '[<>]'; then bad "$label has < or > in its text (write some_var, not <some_var>)"; cfgok=false; break; fi
	done
	if [ "$ct" = TZ ]; then
		if [ -z "$cdef$cval" ]; then bad "$label: an empty TZ overrides the time zone Unraid injects; drop the entry"; cfgok=false; else warn "$label: Unraid already injects TZ; a TZ entry overrides the server's time zone"; fi
	fi
	if printf '%s\n' "$names" | grep -qxF -- "$cn"; then bad "$label: duplicate Name"; cfgok=false; fi
	if printf '%s\n' "$targets" | grep -qxF -- "$ctype:$ct"; then bad "$label: duplicate $ctype Target $ct"; cfgok=false; fi
	names="$names
$cn"
	targets="$targets
$ctype:$ct"
	i=$((i + 1))
done
[ "$cfgok" = false ] || ok "$n Config entries: valid Type/Display/Required/Mask/Mode, unique, no tag-like text"

# ---- Dupearr's own required entries
need() { # need TYPE TARGET WHAT
	if [ "$(count "/Container/Config[@Type='$1' and @Target='$2']")" -ge 1 ]; then ok "Dupearr: $3 ($1 $2)"; else bad "Dupearr: $3 missing ($1 $2)"; fi
}
need Port 3873 "web UI port"
need Path /config "appdata"
need Path /data "media mount"
need Variable PUID "PUID"
need Variable PGID "PGID"
need Variable UMASK "UMASK"
need Variable DUPEARR__AUTH__TRUSTEDPROXIES "trusted proxies"
need Variable DUPEARR__AUTH__ALLOWEDHOSTS "allowed hosts"
[ "$(x "string(/Container/Config[@Target='PUID']/@Default)")" = 99 ] || warn "PUID default is not 99 (Unraid's nobody)"
[ "$(x "string(/Container/Config[@Target='PGID']/@Default)")" = 100 ] || warn "PGID default is not 100 (Unraid's users)"
if [ "$(count "/Container/Config[@Target='TZ']")" -eq 0 ]; then ok "no TZ entry (Unraid injects the server's TZ)"; fi

# ------------------------------------------------------------------------------ repository
if [ -n "$repo" ]; then
	if [ ! -d "$repo" ]; then
		bad "repository directory not found: $repo"
	else
		if git -C "$repo" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
			files=$(git -C "$repo" ls-files)
			info "repository files: git ls-files in $repo (what a clone of the committed tree contains)"
		else
			files=$(cd "$repo" && find . -type f ! -path './.git/*' | sed 's#^\./##')
			info "repository files: every file under $repo (not a git work tree)"
		fi
		if dep=$(printf '%s\n' "$files" | grep -E '(^|/)deprecated/'); then
			bad "a folder named deprecated/ exists (CA deprecates everything in it): $(printf '%s' "$dep" | head -n 3 | tr '\n' ' ')"
		else
			ok "no deprecated/ folder"
		fi
		lic=''
		for f in LICENSE LICENSE.md LICENSE.txt COPYING; do
			[ -f "$repo/$f" ] && { lic=$f; break; }
		done
		if [ -z "$lic" ]; then
			bad "no LICENSE at the repository root (CA requires an OSI-approved license)"
		elif grep -q 'GNU GENERAL PUBLIC LICENSE' "$repo/$lic" && grep -q 'Version 3' "$repo/$lic"; then
			ok "$lic at the root: GNU GPL v3 (OSI-approved)"
		elif grep -Eq 'MIT License|Apache License|Mozilla Public License|BSD' "$repo/$lic"; then
			ok "$lic at the root (OSI-approved family)"
		else
			warn "$lic at the root: check that GitHub detects an OSI-approved SPDX id"
		fi
		if printf '%s\n' "$files" | grep -qx 'ca_profile.xml'; then
			ok "ca_profile.xml at the root is committed"
		elif [ -f "$repo/ca_profile.xml" ]; then
			warn "ca_profile.xml exists at the root but is not committed"
		else
			warn "no ca_profile.xml at the root yet: run make ca-profile and commit it (the submission cannot be finalised without it)"
		fi
		dupes='' others=''
		for f in $(printf '%s\n' "$files" | grep -Ei '\.xml$' || :); do
			[ "$f" != "$as" ] || continue
			[ "$f" != ca_profile.xml ] || continue
			p="$repo/$f"
			[ -f "$p" ] || continue
			cnt=$(x 'count(/Container/Repository | /Container/PluginURL)' "$p")
			if [ "${cnt:-0}" = 0 ]; then
				warn "$f is an .xml file without <Repository>: CA lists it as 'Not an unRaid Application' (harmless, but noisy)"
				continue
			fi
			on=$(x 'string(/Container/Name)' "$p")
			if [ "$on" = "$name" ]; then dupes="$dupes $f"; else others="$others $f($on)"; fi
			if [ "$private" != true ] && grep -Eqi -- "$PRIVATE_RE" "$p"; then bad "$f: another template with private addresses would be published"; fi
		done
		if [ -n "$dupes" ]; then
			bad "another template named '$name' is committed:$dupes (CA scans every .xml and drops one of two templates sharing a name; the LAN variant must never be committed)"
		else
			ok "exactly one committed template named '$name' ($as)"
		fi
		[ -z "$others" ] || info "other templates CA would also list:$others"
		if printf '%s\n' "$files" | grep -qx 'unraid/ca/private.env'; then
			warn "unraid/ca/private.env is committed (it is meant to stay local; git rm --cached it)"
		fi
		if printf '%s\n' "$files" | grep -q '^unraid/ca/out/'; then
			bad "rendered files under unraid/ca/out/ are committed (git rm --cached them)"
		fi
	fi
fi

summary || exit 1
