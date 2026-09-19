def catalog:
	[
		"\(._embedded.firmware | length) firmwares",
		"\([._embedded.firmware[].product] | unique | length) products",
		([._embedded.firmware[].channel] | unique | sort | join("/"))
	]
	| join(", ");

def majors:
	[.[].version | split(".")[0] + ".x"]
	| group_by(.)
	| map("\(length) \(.[0])")
	| join(", ");

def index:
	.data.releases.items as $all
	| [$all[] | select(.slug | startswith("UniFi-OS-"))] as $os
	| [
		"\($all | length) releases",
		"stages \([$all[].stage] | unique | sort | join("/"))",
		"\($os | length) UniFi OS on \($os | majors)"
	]
	| join(", ");

def release:
	.data.release
	| "\(.title) \(.version), boards \([.links[].title] | join("/"))";

def failure:
	.error // (.errors | map(.message) | join("; "));

if $kind == "catalog" then
	catalog
elif $kind == "index" then
	index
elif $kind == "release" then
	release
else
	failure
end
