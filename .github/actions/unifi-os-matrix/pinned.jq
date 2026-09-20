# Pairs each pinned build with the newest release the catalog has for that
# console. Early Access is announced on community.ui.com and never indexed by
# the firmware API, so those builds can only be named by hand.
#
# Input is the release-channel catalog response; $loaded is pinned.json.
$loaded[0] as $pinned |
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
	}
] as $catalog |
[
	$pinned[] |
	. as $build |
	($build.platform | ascii_downcase) as $model |
	select($wanted == "all" or $model == $wanted) |
	(
		[$catalog[] | select(.model == $model)] |
		group_by(.version) |
		map(max_by(.created)) |
		sort_by(.order) |
		last
	) as $from |
	if $from == null then
		error("no released firmware for \($model) to upgrade from")
	else
		{
			model: $model,
			channel: "early-access",
			firmwares: [
				{board: $from.board, version: $from.version, url: $from.url, sha256: $from.sha256},
				{board: $build.platform, version: $build.version, url: $build.url, sha256: $build.sha256}
			],
			from_image: "\($image):\($model)-\($from.version)",
			to_image: "\($image):\($model)-\($build.version)"
		}
	end
]
