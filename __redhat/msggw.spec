%define debug_package   %{nil}
%define _build_id_links none
%define _name msggw
%define _prefix /opt
%define _bindir %{_prefix}/sbin
%define _version 0.7.0
%define _rel 1
%define _arch x86_64
%define _binaryname message-gateway

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
* Sat Aug 22 2026 Binary package builder <builder@famillegratton.net> 0.5.0-2
- bug(BUILDERS): binary should be installed in /opt/sbin, not /opt/bin

* Sat Aug 22 2026 Binary package builder <builder@famillegratton.net> 0.5.0-1
- Merge branch 'develop'
- chore: buildeps update, version bump, doc update
- feat(storage): back-ends are now multi-tenants aware, even if currently unused
- chore: update changelog for 0.4.0-1

* Sat Aug 22 2026 Binary package builder <builder@famillegratton.net> 0.4.0-1
- Merge branch 'develop'
- Version bump
- feat(config): configuration now resolves various URI schemes
- chore: update changelog for 0.2.0-1

* Sat Aug 22 2026 Binary package builder <builder@famillegratton.net> 0.2.0-1
- version bump
- feature(config): config file now supports multiple backends
- chore: updated sample file so it reflects the availability of a new PGSQL backend
- feat: removing mentions of FB messenger support; Meta makes it too hard to support
- chore: changed the binary name being built
- bug(ARCHBUILDER): fixed cleanup steps in Makefile
- APKBUILDER: renamed pre/post scripts
- completed tool rebranding
- Merge remote-tracking branch 'refs/remotes/origin/develop' into develop
- go version bump: 1.26.6 -> 1.27.0
- chore: update changelog for 0.0.1-1

* Fri Aug 21 2026 Binary package builder <builder@famillegratton.net> 0.0.1-1
- initial stub

