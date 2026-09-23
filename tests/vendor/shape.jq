def slug_version:
	gsub("[^0-9]+"; "-") | rtrimstr("-");

def family:
	. as $release | $release.slug | rtrimstr("-" + ($release.version | slug_version));

def order:
	[.version | scan("[0-9]+") | tonumber];

# The API answers some releases twice under two ids, identical down to their
# download links.
def index:
	.data.releases.items
	| map(. + {family: family})
	| group_by(.family)
	| map({
		key: .[0].family,
		value: (
			map({version, stage, id})
			| unique_by([.version, .stage])
			| sort_by(order, .stage)
			| reverse
		)
	})
	| sort_by(.key)
	| from_entries;

if $kind == "index" then
	index
else
	.
end
