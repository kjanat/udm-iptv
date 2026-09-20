package ui

type helpEntry struct {
	title, text string
}

// Plain-language help per field key, shown in a popup on F1 or ?.
var fieldHelp = map[string]helpEntry{
	"country": {"Country", `Pick the country of your TV subscription. The next question only shows providers known to work there.

Start typing to narrow the list. Backspace corrects, Esc clears.

All countries shows every provider at once. Other means your provider is not listed. You then fill in every setting yourself.`},
	"provider": {"TV provider", `Pick the company you pay for TV. This fills in the settings known to work for that provider. You can still change every setting on the next pages.

A provider with more than one TV network gets one row per network. Pick the one for your region. Your provider's welcome letter usually names it.

Start typing to narrow the list.`},
	"wan-port": {"Internet port", `The port on the router where the cable from your modem or fibre box plugs in.

IPTV travels over the same cable as your internet. So this is where the TV traffic arrives.

Connected only means a cable is plugged in and a link was detected.

Enter another interface manually… lets you pick or type a port not in the list.`},
	"vlan": {"IPTV VLAN ID", `Many providers send TV over a separate numbered lane on the same cable. That lane is called a VLAN.

The number comes from your provider. KPN uses 4 for example.

Enter 0 if your provider sends TV without a VLAN.`},
	"dhcp": {"DHCP for IPTV", `DHCP means the provider hands your router an address for the TV lane automatically. Almost every provider does this.

Choose No only if your provider gave you a fixed address to type in.`},
	"vlan-interface": {"VLAN interface name", `The name this program gives to the TV lane on the router, such as iptv.

It is not a physical port. It only shows up in the router's own network list.

The default is fine.`},
	"vlan-mac": {"Custom MAC address", `A hardware address the provider may expect to see on the TV lane.

Leave it empty unless your provider explicitly told you to use one. Usually that is the address of the box they supplied.`},
	"dhcp-options": {"DHCP client options", `Enter flags and values separated by spaces, without "udhcpc". Example (KPN): -O staticroutes -V IPTV_RG

-O OPTION     Ask the provider for a DHCP option; repeatable.
              Example: -O staticroutes requests network routes.
-V TEXT       Identify the device type to the provider.
              Example: -V IPTV_RG (required by the KPN profile).
-H NAME       Send a hostname, for example -H tv-router.
-r ADDRESS    Request an IP; the provider may offer another.
-C            Omit the default client identifier.
-o            Request only options explicitly listed with -O.

Keep your profile's values unless your provider says otherwise. Empty means no extra arguments, not "restore profile defaults". No shell syntax: quotes do not group words; no variable expansion. Values containing spaces cannot be entered in this field.

Do not override -i, -s, -p, -f, -R or -t/-T/-A: udm-iptv manages the interface, lease hook, process and retry timing. Avoid -b/-n/-q: they change when the client backgrounds or exits, breaking its lifecycle. These flags are passed through, not blocked by this input.

Available flags vary by firmware. On the router, run udhcpc --help (or busybox udhcpc --help) for the installed client's full list.`},
	"dhcp-routes": {"Access to TV services", `Your provider can tell the router how to reach its TV services. This is separate from receiving the live TV stream.

TV services only: use those directions for the networks the provider lists, such as the TV guide and on-demand servers. Do not accept a catch-all path that sends other internet traffic through IPTV. Recommended for most setups.

Also allow other internet traffic: accept that catch-all path too. Browsing and apps could then use IPTV instead of your normal internet connection and stop working. Choose this only when your provider explicitly requires it.

Already handled separately: ignore the provider's directions. Use this only if access is configured elsewhere; otherwise the guide or replay may fail.

Technically, these directions are DHCP routes. This does not block traffic or replace firewall rules.`},
	"static-address": {"Static IPTV address", `The fixed address your provider gave you for the TV lane. Write it with its network size, for example 10.0.0.2/24.

The part after the slash tells the router how large the provider's network is.`},
	"lan": {"TV networks", `The home networks where your TV boxes are connected, by cable or Wi-Fi. br0 is the default LAN.

Move with the arrow keys. Press space or x to tick or untick a network. Press Enter when every network with a TV box is ticked.

Networks not ticked cannot receive TV streams.

Enter another interface manually… opens a small picker. It lists the router's other interfaces, or you type a name.`},
	"nat": {"IPTV unicast destinations", `Besides live TV, TV boxes talk to provider servers. They fetch the programme guide, video on demand and pause features from there.

Those servers live in these network ranges. The router forwards that traffic over the TV lane.

The values come from your provider profile. Each input holds one network. Use Ctrl+N to add a box, Ctrl+D to remove the selected box, and arrow keys to move between boxes. Press Enter to keep the list and continue.`},
	"proxy": {"Multicast proxy", `Live TV arrives as multicast: one stream shared by every viewer. A proxy passes those streams from the TV lane to your home network.

improxy is the newer option and works on current UniFi OS. igmpproxy is the older one.`},
	"igmp": {"IGMP version", `IGMP is the protocol a TV box uses to ask for a channel.

Version 3 is current and works with almost every box. Pick version 2 only if your provider or box needs it.`},
	"mld": {"IPv6 multicast", `IGMP handles IPv4 multicast. MLD adds IPv6 multicast.

Choose IPv4 only unless your provider also carries IPTV over IPv6. For both, use MLDv2 unless the provider specifically requires MLDv1. Enabling IPv6 limits the following proxy choice to improxy.

IPv4 remains enabled; this setting does not select IPv6-only operation.`},
	"quickleave": {"Quickleave", `With quickleave the router stops a stream the moment a TV box switches channel. That saves bandwidth.

When several boxes share one network, a channel can drop for the others. Leave it off in that case.`},
	"debug": {"Proxy debug logs", `Writes detailed messages from the multicast proxy to the log.

Useful while investigating a problem. Noisy otherwise. Turn it off again when you are done.`},
	"proxy-sources": {"Allowed multicast sources", `igmpproxy only forwards streams that come from these network ranges. Your provider's TV servers live there. improxy has no such filter and ignores this list.

0.0.0.0/0 allows every source. That is the unrestricted choice; use it when your provider's ranges are unknown.`},
	"telemetry": {"Help improve udm-iptv", `Sends failures and the settings you chose to the maintainer's Sentry project over HTTPS, on EU servers. It improves provider profiles and shows which firmware builds break IPTV.

These reports are not anonymous.

Every report carries a random installation ID that stays the same, so your settings history and your failures are linked to one router.

Network identity is on by default. It adds your full public IP address and its reverse-DNS hostname, and the lookup contacts ipify and your DNS resolver.

Never sent: passwords, packet captures.

Answer No to send nothing. Change it later with udm-iptv configure, one category at a time. docs/telemetry.md lists every field.`},
	"accept": {"Review", `A summary of everything you chose.

Continue applies the settings. Cancel throws the changes away and keeps the current configuration.`},
}
