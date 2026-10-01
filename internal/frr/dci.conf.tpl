{{- /* Rendered in FRR's canonical "show running-config" form, so that drift
       detection can compare it line by line with the running configuration.
       .TransportVRF set: SRv6 transport in an EVPN VRF (DCI network) joined via
       a veth pair; empty: transport in the default VRF.
       Every network gets its FRR VRF with the L3VNI and a complete BGP
       instance that announces its routes as EVPN type-5. */ -}}
{{ range $n := .Networks -}}
vrf {{ $n.VRF }}
 vni {{ $n.VNI }}
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
{{- if not .TransportVRF }}
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
  rd vpn export {{ $.RD $n.VRF }}
  rt vpn both {{ $.RT $n.VRF }}
  route-map vpn import {{ filter $n.VRF (familyOf $af) }}
  route-map vpn export {{ filter $n.VRF (familyOf $af) }}
  export vpn
  import vpn
 exit-address-family
{{- end }}
 address-family l2vpn evpn
  advertise ipv4 unicast
  advertise ipv6 unicast
 exit-address-family
exit
!
{{- end }}
{{- if .TransportVRF }}
router bgp {{ .ASN }} vrf {{ .TransportVRF }}
 address-family ipv6 unicast
  redistribute static
 exit-address-family
exit
!
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
exit
{{- else }}
route-map {{ $f.Name }} deny 10
exit
{{- end }}
!
{{- end }}
{{- /* Peers: only routes with a configured route target are accepted. */}}
{{- range $i, $rt := .RTs }}
bgp extcommunity-list standard {{ rtList }} seq {{ seq $i }} permit rt {{ $rt }}
{{- end }}
route-map {{ peerIn }} permit 10
 match extcommunity {{ rtList }}
exit
