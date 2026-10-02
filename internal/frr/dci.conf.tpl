{{- /* Rendered in FRR's canonical "show running-config" form, so that drift
       detection can compare it line by line with the running configuration.
       .TransportVRF set: SRv6 transport in an EVPN VRF (DCI network) joined via
       a veth pair; empty: transport in the default VRF.
       Every network gets its FRR VRF with the L3VNI and a complete BGP
       instance that announces its routes as EVPN type-5.
       .Withhold leaves out the announcement of locator and loopback; .Drain
       additionally the type-5 announcement of the tenant VRFs.
       Active aggregates (with host routes from the own fabric in their range,
       found by open-dci) are announced from a blackhole route in the VRF,
       marked so that they don't go back into the own partition as type-5; the
       VPN export removes the mark again. FRR's aggregate-address isn't used:
       10.4.1 leaves its VPN copy behind when it goes away. */ -}}
{{ range $n := .Networks -}}
vrf {{ $n.VRF }}
 vni {{ $n.VNI }}
{{- range $.Aggregates $n.VRF "" }}
 {{ if .Addr.Is4 }}ip{{ else }}ipv6{{ end }} route {{ . }} blackhole
{{- end }}
exit-vrf
!
{{ end -}}
segment-routing
 srv6
  encapsulation
   source-address {{ .Loopback }}
  exit
  locators
   locator {{ .LocatorName }}
    prefix {{ .Locator }} block-len {{ .BlockLen }} node-len {{ .NodeLen }}
   exit
  exit
 exit
exit
!
router bgp {{ .ASN }}
{{- if .InterfacePeers }}
 bgp disable-ebgp-connected-route-check
{{- end }}
{{- range .Peers }}{{ if .Address }}
 neighbor {{ .Address }} remote-as {{ .ASN }}
{{- if ne .ASN $.ASN }}
 neighbor {{ .Address }} ebgp-multihop 16
{{- end }}
 neighbor {{ .Address }} update-source {{ $.Loopback }}
 neighbor {{ .Address }} capability extended-nexthop
{{- end }}{{ end }}
 segment-routing srv6
  locator {{ .LocatorName }}
 exit
{{- if .AddressPeers }}
 address-family ipv4 unicast
{{- range .Peers }}{{ if .Address }}
  no neighbor {{ .Address }} activate
{{- end }}{{ end }}
 exit-address-family
{{- end }}
 address-family ipv4 vpn
{{- range .Peers }}
  neighbor {{ .Neighbor }} activate
  neighbor {{ .Neighbor }} route-map {{ peerIn }} in
  neighbor {{ .Neighbor }} maximum-prefix {{ .MaxPrefixes }}
{{- end }}
 exit-address-family
 address-family ipv6 vpn
{{- range .Peers }}
  neighbor {{ .Neighbor }} activate
  neighbor {{ .Neighbor }} route-map {{ peerIn }} in
  neighbor {{ .Neighbor }} maximum-prefix {{ .MaxPrefixes }}
{{- end }}
 exit-address-family
{{- if and (not .TransportVRF) (not .Withhold) }}
 address-family ipv6 unicast
  network {{ .Locator }}
{{- if .Anycast }}
  network {{ .Loopback }}/128
{{- end }}
 exit-address-family
{{- end }}
exit
!
{{- range $n := .Networks }}
router bgp {{ $.ASN }} vrf {{ $n.VRF }}
 bgp router-id {{ $.RouterID }}
 sid vpn per-vrf export {{ $n.SID }}
{{- range $af := list "ipv4" "ipv6" }}
 address-family {{ $af }} unicast
{{- range $.Aggregates $n.VRF (familyOf $af) }}
  network {{ . }} route-map {{ aggMap }}
{{- end }}
  rd vpn export {{ $.RD $n.VRF }}
  rt vpn both {{ $.RT $n.VRF }}
  route-map vpn import {{ filter $n.VRF (familyOf $af) }}
  route-map vpn export {{ filter $n.VRF (familyOf $af) }}
  export vpn
  import vpn
 exit-address-family
{{- end }}
{{- if not $.Drain }}
 address-family l2vpn evpn
  advertise ipv4 unicast{{ if $n.Aggregates }} route-map {{ advMap }}{{ end }}
  advertise ipv6 unicast{{ if $n.Aggregates }} route-map {{ advMap }}{{ end }}
 exit-address-family
{{- end }}
exit
!
{{- end }}
{{- if .TransportVRF }}
{{- if not .Withhold }}
router bgp {{ .ASN }} vrf {{ .TransportVRF }}
 address-family ipv6 unicast
  redistribute static
 exit-address-family
exit
!
{{- end }}
ipv6 route {{ .LocatorBlock }} {{ .VethPeerLL }} {{ .Veth }}
{{- end }}
ipv6 route {{ .Locator }} blackhole
{{- if .TransportVRF }}
!
vrf {{ .TransportVRF }}
 ipv6 route {{ .Locator }} {{ .VethLL }} {{ .VethPeer }}
{{- if .Anycast }}
 ipv6 route {{ .Loopback }}/128 {{ .VethLL }} {{ .VethPeer }}
{{- end }}
exit-vrf
{{- end }}
!
{{- /* Prefix allowlists: only these routes leave or enter a tenant VRF. */}}
{{- range $f := .Filters }}
{{- range $i, $e := $f.Entries }}
{{ $f.PrefixList }} prefix-list {{ $f.Name }} seq {{ seq $i }} permit {{ $e }}
{{- end }}
{{- if $f.Entries }}
route-map {{ $f.Name }} permit 10
 match {{ $f.PrefixList }} address prefix-list {{ $f.Name }}
{{- if $f.DeleteMarker }}
 set large-comm-list {{ aggList }} delete
{{- end }}
exit
{{- else }}
route-map {{ $f.Name }} deny 10
exit
{{- end }}
!
{{- end }}
{{- if .Aggregating }}
bgp large-community-list standard {{ aggList }} seq 5 permit {{ .ASN }}:0:1
route-map {{ aggMap }} permit 10
 set large-community {{ .ASN }}:0:1
exit
!
route-map {{ advMap }} deny 10
 match large-community {{ aggList }}
exit
!
route-map {{ advMap }} permit 20
exit
!
{{- end }}
{{- /* Peers: only routes with a configured route target are accepted. */}}
{{- range $i, $rt := .RTs }}
bgp extcommunity-list standard {{ rtList }} seq {{ seq $i }} permit rt {{ $rt }}
{{- end }}
route-map {{ peerIn }} permit 10
 match extcommunity {{ rtList }}
exit
