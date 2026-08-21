%define debug_package   %{nil}
%define _build_id_links none
%define _name msggw
%define _prefix /opt
%define _version 0.0.1
%define _rel 1
%define _arch x86_64
%define _binaryname msg-gw

Name:       msggw
Version:    %{_version}
Release:    %{_rel}
Summary:    Messages <-> Mattermost bridge

Group:      Messaging tools
License:    GPL-3.0-or-later
URL:        https://git.famillegratton.net:3000/mainline/msggw.git

Source0:    %{name}-%{_version}.tar.gz
#BuildArchitectures: x86_64
BuildRequires: gcc
#Requires: sudo
#Obsoletes: vmman1 > 1.140

%description
Messages <-> Mattermost bridge

%prep
%autosetup

%build
cd src
go mod download
PATH=$PATH:/opt/go/bin CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o %{_builddir}/%{name}-%{version}/%{_binaryname} .

%clean
rm -rf $RPM_BUILD_ROOT

%pre

%install
rm -rf %{buildroot}
install -Dpm 0755 %{_builddir}/%{name}-%{version}/%{_binaryname} %{buildroot}%{_bindir}/%{_binaryname}

%post

%preun

%postun

%files
%defattr(0755,root,root,-)
%{_bindir}/%{_binaryname}


%changelog
* Fri Aug 21 2026 Binary package builder <builder@famillegratton.net> 0.0.1-1
- initial stub

