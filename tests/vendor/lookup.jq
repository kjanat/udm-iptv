def slug_version:
	gsub("[^0-9]+"; "-") | rtrimstr("-");

first(
	to_entries[]
	| .key as $family
	| .value[]
	| select($family + "-" + (.version | slug_version) == $slug)
	| "\(.id) \(.version)"
)
