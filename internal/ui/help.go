package ui

type helpEntry struct {
	title, text string
}

// Plain-language help per field key, shown by Frame on F1.
var fieldHelp = map[string]helpEntry{
	"country": {"Country", `Pick the country of your TV subscription.
The next question only shows providers known to work there.

Start typing to narrow the list.
Backspace corrects, Esc clears.

All countries shows every provider at once.
Other means your provider is not listed. You then fill in every setting yourself.`},
	"provider": {"TV provider", `Pick the company you pay for TV.
This fills in the settings known to work for that provider.
You can still change every setting on the next pages.

A provider with more than one TV network gets one row per network.
Pick the one for your region. Your provider's welcome letter usually names it.

Start typing to narrow the list.`},
	"wan-port": {"Internet port", `The port on the router where the cable from your modem or fibre box plugs in.

IPTV travels over the same cable as your internet.
So this is where the TV traffic arrives.

Connected only means a cable is plugged in and a link was detected.

Enter another interface manually… lets you pick or type a port not in the list.`},
	"vlan": {"IPTV VLAN ID", `Many providers send TV over a separate numbered lane on the same cable.
That lane is called a VLAN.

The number comes from your provider. KPN uses 4 for example.

Enter 0 if your provider sends TV without a VLAN.`},
	"dhcp": {"DHCP for IPTV", `DHCP means the provider hands your router an address for the TV lane automatically.
Almost every provider does this.

Choose No only if your provider gave you a fixed address to type in.`},
	"vlan-interface": {"VLAN interface name", `The name this program gives to the TV lane on the router, such as iptv.

It is not a physical port.
It only shows up in the router's own network list.

The default is fine.`},
	"vlan-mac": {"Custom MAC address", `A hardware address the provider may expect to see on the TV lane.

Leave it empty unless your provider explicitly told you to use one.
Usually that is the address of the box they supplied.`},
	"dhcp-options": {"DHCP client options", `Extra arguments for udhcpc.
That is the small program that asks your provider for an address.

The defaults from your provider profile are usually right.
Change these only if your provider documents specific options.`},
	"dhcp-routes": {"Routes from the lease", `The provider tells the router which networks the TV lane reaches.

Never a default route is right almost everywhere. The TV lane still gets the
networks your provider lists, but it cannot become the way out for everything.
Including a default route can send all your internet traffic through the TV
lane, so pick it only when your provider says to.
No routes suits a lane that should carry multicast and nothing else.`},
	"static-address": {"Static IPTV address", `The fixed address your provider gave you for the TV lane.
Write it with its network size, for example 10.0.0.2/24.

The part after the slash tells the router how large the provider's network is.`},
	"lan": {"TV networks", `The home networks where your TV boxes are connected, by cable or Wi-Fi.
br0 is the default LAN.

Move with the arrow keys.
Press space or x to tick or untick a network.
Press Enter when every network with a TV box is ticked.

Networks not ticked cannot receive TV streams.

Enter another interface manually… opens a small picker.
It lists the router's other interfaces, or you type a name.`},
	"nat": {"IPTV unicast destinations", `Besides live TV, TV boxes talk to provider servers.
They fetch the programme guide, video on demand and pause features from there.

Those servers live in these network ranges.
The router forwards that traffic over the TV lane.

The values come from your provider profile.`},
	"proxy": {"Multicast proxy", `Live TV arrives as multicast: one stream shared by every viewer.
A proxy passes those streams from the TV lane to your home network.

improxy is the newer option and works on current UniFi OS.
igmpproxy is the older one.`},
	"igmp": {"IGMP version", `IGMP is the protocol a TV box uses to ask for a channel.

Version 3 is current and works with almost every box.
Pick version 2 only if your provider or box needs it.`},
	"quickleave": {"Quickleave", `With quickleave the router stops a stream the moment a TV box switches channel.
That saves bandwidth.

When several boxes share one network, a channel can drop for the others.
Leave it off in that case.`},
	"debug": {"Proxy debug logs", `Writes detailed messages from the multicast proxy to the log.

Useful while investigating a problem. Noisy otherwise.
Turn it off again when you are done.`},
	"proxy-sources": {"Allowed multicast sources", `igmpproxy only forwards streams that come from these network ranges.
Your provider's TV servers live there.

0.0.0.0/0 allows every source.
That is the safe choice if you are unsure.`},
	"telemetry": {"Help improve udm-iptv", `Sends failures and the settings you chose to the
maintainer's Sentry project over HTTPS, on EU servers.
It improves provider profiles and shows which firmware
builds break IPTV.

These reports are not anonymous.

Every report carries a random installation ID that stays
the same, so your settings history and your failures are
linked to one router.

Network identity is on by default. It adds your full
public IP address and its reverse-DNS hostname, and the
lookup contacts ipify and your DNS resolver.

Never sent: passwords, MAC addresses, interface names,
your configured addresses, proxy logs, captures.

Answer No to send nothing.
Change it later with udm-iptv configure, one category at
a time. docs/telemetry.md lists every field.`},
	"accept": {"Review", `A summary of everything you chose.

Continue applies the settings.
Cancel throws the changes away and keeps the current configuration.`},
}
