{{- /* Rendered in FRR's canonical "show running-config" form, so that drift
       detection can compare it line by line with the running configuration.
       .TransportVRF set: SRv6 transport in an EVPN VRF (DCI network) joined via
       a veth pair; empty: transport in the default VRF. */ -}}
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
{{- range .Peers }}
 neighbor {{ .Address }} remote-as {{ .ASN }}
{{- if ne .ASN $.ASN }}
 neighbor {{ .Address }} ebgp-multihop 16
{{- end }}
 neighbor {{ .Address }} update-source {{ $.Loopback }}
 neighbor {{ .Address }} capability extended-nexthop
{{- end }}
 segment-routing srv6
  locator {{ .LocatorName }}
 exit
 address-family ipv4 unicast
{{- range .Peers }}
  no neighbor {{ .Address }} activate
{{- end }}
 exit-address-family
 address-family ipv4 vpn
{{- range .Peers }}
  neighbor {{ .Address }} activate
{{- end }}
 exit-address-family
 address-family ipv6 vpn
{{- range .Peers }}
  neighbor {{ .Address }} activate
{{- end }}
 exit-address-family
{{- if not .TransportVRF }}
 address-family ipv6 unicast
  network {{ .Locator }}
 exit-address-family
{{- end }}
exit
!
{{- range $n := .Networks }}
router bgp {{ $.ASN }} vrf {{ $n.VRF }}
 sid vpn per-vrf export auto
{{- range $af := list "ipv4" "ipv6" }}
 address-family {{ $af }} unicast
  rd vpn export {{ $.RD $n.VRF }}
  rt vpn both {{ $.RT $n.VRF }}
  export vpn
  import vpn
 exit-address-family
{{- end }}
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
exit-vrf
{{- end }}
