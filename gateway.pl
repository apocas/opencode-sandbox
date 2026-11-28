#!/usr/bin/perl
# The half of opencode-sandbox's gateway that runs inside a sandbox. The other
# half runs on your machine, where your providers' API keys stay; it starts
# this one with the engine's exec, so the two talk over stdin and stdout and
# nothing listens on a network outside the sandbox.
#
#   serve PORT          listen on 127.0.0.1:PORT and relay each connection
#   wait PORT SECONDS   wait until something listens there (exit 1 if not)
#   connect HOST PORT   reach HOST:PORT through the gateway, for a program
#                       that can't use a proxy by itself: what it writes here
#                       goes there and back (ssh's ProxyCommand)
#
# What's relayed goes in frames: a connection's number (4 bytes), a type
# (1 byte) and the length of the data (4 bytes), big-endian, then the data.
# READY says this is listening; OPEN, DATA and CLOSE are about a connection.
use strict;
use warnings;
use IO::Socket::INET;
use IO::Select;
use Socket qw(IPPROTO_TCP TCP_NODELAY);

use constant { READY => 0, OPEN => 1, DATA => 2, CLOSE => 3, MAX => 32768 };
# How long one connection's reader may keep the others waiting.
use constant STALL => 30;

my ($mode, $port, $secs) = @ARGV;
connect_through(@ARGV[1, 2]) if ($mode // '') eq 'connect';
die "usage: $0 serve PORT | wait PORT SECONDS | connect HOST PORT\n"
  unless ($mode // '') =~ /^(serve|wait)$/ && ($port // '') =~ /^[0-9]+$/;

if ($mode eq 'wait') {
  for (1 .. 20 * ($secs // 5)) {
    exit 0 if IO::Socket::INET->new(PeerAddr => '127.0.0.1', PeerPort => $port, Timeout => 1);
    select undef, undef, undef, 0.05;
  }
  exit 1;
}

$SIG{PIPE} = 'IGNORE';
binmode STDIN;
binmode STDOUT;
my $listener = IO::Socket::INET->new(
  Listen => 128, LocalAddr => '127.0.0.1', LocalPort => $port, ReuseAddr => 1, Proto => 'tcp',
) or die "can't listen on 127.0.0.1:$port: $!\n";
my $sel = IO::Select->new($listener, \*STDIN);
my (%sock, %num);    # connections by number, numbers by file descriptor
my ($next, $in) = (0, '');

# put writes all of the data; false if it can't. A connection of the
# sandbox's (they don't block) gets STALL seconds to take what it's sent:
# the other connections wait behind it.
sub put {
  my ($fh, $data) = @_;
  my $off = 0;
  while ($off < length $data) {
    my $n = syswrite $fh, $data, length($data) - $off, $off;
    if (!defined $n) {
      next if $!{EINTR};
      return 0 unless $!{EAGAIN} || $!{EWOULDBLOCK};
      return 0 unless IO::Select->new($fh)->can_write(STALL);
      next;
    }
    $off += $n;
  }
  return 1;
}

# Without the other half there's nothing to do here.
sub frame { put(\*STDOUT, pack('NCN', $_[0], $_[1], length $_[2]) . $_[2]) or exit 0 }

# drop forgets a connection, and says so unless the other half did.
sub drop {
  my ($n, $tell) = @_;
  my $s = delete $sock{$n} or return;
  delete $num{fileno $s};
  $sel->remove($s);
  close $s;
  frame($n, CLOSE, '') if $tell;
}

frame(0, READY, '');
while (1) {
  for my $fh ($sel->can_read) {
    my $fd = fileno $fh;
    next unless defined $fd;    # dropped earlier in this round
    if ($fh == $listener) {
      my $s = $listener->accept or next;
      setsockopt $s, IPPROTO_TCP, TCP_NODELAY, 1;
      $s->blocking(0);
      my $n = ++$next;
      $sock{$n} = $s;
      $num{fileno $s} = $n;
      $sel->add($s);
      frame($n, OPEN, '');
    } elsif ($fd == 0) {
      sysread STDIN, $in, 65536, length $in or exit 0;    # the other half went away
      while (length $in >= 9) {
        my ($n, $type, $len) = unpack 'NCN', $in;
        die "a frame of $len bytes\n" if $len > MAX;
        last if length $in < 9 + $len;
        my $data = substr $in, 9, $len;
        substr($in, 0, 9 + $len) = '';
        my $s = $sock{$n} or next;
        if ($type == DATA) { put($s, $data) or drop($n, 1) }
        elsif ($type == CLOSE) { drop($n, 0) }
      }
    } else {
      my $n = $num{$fd} // next;
      my $got = sysread $fh, my $buf, MAX;
      if ($got) { frame($n, DATA, $buf) }
      elsif (defined $got || !($!{EAGAIN} || $!{EWOULDBLOCK} || $!{EINTR})) { drop($n, 1) }
    }
  }
}

# connect_through reaches a host through the gateway's port here, which lets
# a sandbox with a filtered network out to the hosts on its list, and relays
# stdin and stdout to it. If the host isn't allowed, it says why and fails.
sub connect_through {
  my ($host, $hport) = @_;
  die "usage: $0 connect HOST PORT\n" unless defined $host && ($hport // '') =~ /^[0-9]+$/;
  my $gw = $ENV{OPENCODE_SANDBOX_PROXY} || 17872;
  my $s = IO::Socket::INET->new(PeerAddr => '127.0.0.1', PeerPort => $gw, Proto => 'tcp')
    or die "opencode-sandbox: can't reach this sandbox's gateway (127.0.0.1:$gw): $!\n";
  setsockopt $s, IPPROTO_TCP, TCP_NODELAY, 1;
  my $to = $host =~ /:/ ? "[$host]:$hport" : "$host:$hport";
  syswrite $s, "CONNECT $to HTTP/1.1\r\nHost: $to\r\n\r\n";
  my $head = '';
  until ($head =~ /\r\n\r\n/) {
    sysread $s, $head, 4096, length $head or die "opencode-sandbox: the gateway closed the connection to $to\n";
    die "opencode-sandbox: the gateway's answer makes no sense\n" if length $head > 65536;
  }
  my ($top, $rest) = split /\r\n\r\n/, $head, 2;
  my ($status) = $top =~ m{^HTTP/1\.[01] ([0-9]{3})};
  if (($status // 0) != 200) {
    # Its answer says why, for whoever reads the terminal.
    my ($len) = $top =~ /^Content-Length: *([0-9]+)/mi;
    while (defined $len && length $rest < $len) { sysread $s, $rest, 4096, length $rest or last }
    $rest =~ s/\s+\z//;
    die(($rest ne '' ? $rest : "opencode-sandbox: $to can't be reached from this sandbox") . "\n");
  }
  $SIG{PIPE} = 'IGNORE';
  binmode STDIN;
  binmode STDOUT;
  syswrite STDOUT, $rest if length $rest;
  my $sel = IO::Select->new($s, \*STDIN);
  my $ended = 0;    # our side has no more to say
  while (1) {
    # Once our side has ended, the far end gets a while to finish answering.
    my @ready = $sel->can_read($ended ? 10 : undef) or exit 0;
    for my $fh (@ready) {
      my $n = sysread $fh, my $buf, 65536;
      if (!$n) {
        exit 0 if $fh == $s;    # the far end is done
        $sel->remove($fh);      # ours is: it may still be answered
        $ended = 1;
        next;
      }
      my $out = $fh == $s ? \*STDOUT : $s;
      my $off = 0;
      while ($off < $n) {
        my $w = syswrite $out, $buf, $n - $off, $off;
        exit 0 unless defined $w;
        $off += $w;
      }
    }
  }
}
