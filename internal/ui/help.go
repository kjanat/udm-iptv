package ui

type helpEntry struct {
	title, text string
}

// Plain-language help per field key, shown by Frame on F1.
var fieldHelp = map[string]helpEntry{
	"country":        {"Country", "Pick the country where your TV subscription is. The next question only shows providers known to work there. Choose Other if your country or provider is not listed; you then fill in every setting yourself."},
	"provider":       {"TV provider", "Pick the company you pay for TV. This fills in the settings that are known to work for that provider. You can still change every setting on the next pages."},
	"profile":        {"Network", "Some providers run more than one TV network, each with its own VLAN and addresses. Pick the one that matches your region or the network you were connected on. Your provider's welcome letter or app usually names it."},
	"wan-port":       {"Internet port", "The physical port on the router where the cable from your modem or fibre box plugs in. IPTV travels over the same cable as your internet, so this is where the TV traffic arrives. Connected only means a cable is plugged in and a link was detected."},
	"wan-interface":  {"Interface name", "The router's own name for a port, such as eth8. You can find it in the UniFi interface or by running ip link on the router. Only needed when the port you want is not in the list."},
	"vlan":           {"IPTV VLAN ID", "Many providers send TV over a separate numbered lane on the same cable, called a VLAN. The number is given by your provider. KPN uses 4 for example. Enter 0 if your provider sends TV without a VLAN."},
	"dhcp":           {"DHCP for IPTV", "DHCP means the provider hands your router an address for the TV lane automatically. Almost every provider does this. Choose No only if your provider gave you a fixed address to type in."},
	"vlan-interface": {"VLAN interface name", "The name this program gives to the TV lane on the router, such as iptv. It is not a physical port and only shows up in the router's own network list. The default is fine."},
	"vlan-mac":       {"Custom MAC address", "A hardware address the provider may expect to see on the TV lane. Leave it empty unless your provider explicitly told you to use a specific one, usually the address of the box they supplied."},
	"dhcp-options":   {"DHCP client options", "Extra arguments for udhcpc, the small program that asks your provider for an address. The defaults from your provider profile are usually right. Change these only if your provider documents specific options."},
	"default-route":  {"DHCP default-route fallback", "When the provider hands out a default route on the TV lane, accepting it can send all your internet traffic through the TV lane. Leave this on No unless your provider requires it."},
	"static-address": {"Static IPTV address", "The fixed address your provider gave you for the TV lane, written with its network size, for example 10.0.0.2/24. The part after the slash tells the router how large the provider's network is."},
	"lan":            {"TV networks", "The home networks where your TV boxes are connected, by cable or Wi-Fi. br0 is the default LAN. Move with the arrow keys, press space or x to tick or untick a network, and press Enter when every network with a TV box is ticked. Networks not ticked cannot receive TV streams."},
	"lan-extra":      {"Additional interface names", "Names of extra networks that are not in the list, for example br4 for VLAN 4. Separate several names with spaces or commas."},
	"nat":            {"IPTV unicast destinations", "Besides the live TV streams, TV boxes talk to provider servers for the programme guide, video on demand and pausing. Those servers live in these network ranges. The router forwards that traffic over the TV lane. The values come from your provider profile."},
	"proxy":          {"Multicast proxy", "Live TV arrives as multicast, one stream shared by every viewer. A proxy passes those streams from the TV lane to your home network. improxy is the newer option and works on current UniFi OS. igmpproxy is the older one."},
	"igmp":           {"IGMP version", "IGMP is the protocol a TV box uses to ask for a channel. Version 3 is current and works with almost every box. Pick version 2 only if your provider or box needs it."},
	"quickleave":     {"Quickleave", "With quickleave the router stops a stream the moment a TV box switches channel. That saves bandwidth, but when several boxes share one network a channel can drop for the others. Leave it off in that case."},
	"debug":          {"Proxy debug logs", "Writes detailed messages from the multicast proxy to the log. Useful while investigating a problem, noisy otherwise. Turn it off again when you are done."},
	"proxy-sources":  {"Allowed multicast sources", "igmpproxy only forwards streams that come from these network ranges. Your provider's TV servers live there. 0.0.0.0/0 allows every source and is the safe choice if you are unsure."},
	"telemetry":      {"Help improve udm-iptv", "Sends anonymous error reports and which settings were used to the developer, so defaults and reliability improve. Nothing about your network addresses is sent. This is optional and can be changed later."},
	"accept":         {"Review", "A summary of everything you chose. Continue applies the settings. Cancel throws the changes away and keeps the current configuration."},
}
