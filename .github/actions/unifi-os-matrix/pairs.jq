($cutoff | fromdateiso8601) as $cutoff_epoch |
[
	._embedded.firmware[] |
	select((.created | fromdateiso8601) < $cutoff_epoch) |
	{
		model: (.platform | ascii_downcase),
		board: .platform,
		version: "\(.version_major).\(.version_minor).\(.version_patch)",
		order: [.version_major, .version_minor, .version_patch],
		created: .created,
		url: ._links.data.href,
		sha256: .sha256_checksum
	} |
	select($wanted == "all" or .model == $wanted)
] |
if $required and $wanted != "all" and length == 0 then
	error("unknown sku \($wanted)")
else
	.
end |
group_by(.model) |
map(
	.[0].model as $model |
	group_by(.version) |
	map(max_by(.created)) |
	sort_by(.order) |
	if length < 2 then
		if $required then
			error("fewer than two \($channel) releases before \($cutoff) for \($model)")
		else
			empty
		end
	else
		(.[-2:] | map({board, version, url, sha256})) as $pair |
		{
			model: $model,
			channel: $channel,
			firmwares: $pair,
			from_image: "\($image):\($model)-\($pair[0].version)",
			to_image: "\($image):\($model)-\($pair[1].version)"
		}
	end
)
