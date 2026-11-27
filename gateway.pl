#!/usr/bin/perl
# The half of opencode-sandbox's gateway that runs inside a sandbox. The other
# half runs on your machine, where your providers' API keys stay; it starts
# this one with the engine's exec, so the two talk over stdin and stdout and
# nothing listens on a network outside the sandbox.
#
#   serve PORT          listen on 127.0.0.1:PORT and relay each connection
#   wait PORT SECONDS   wait until something listens there (exit 1 if not)
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

my ($mode, $port, $secs) = @ARGV;
die "usage: $0 serve PORT | wait PORT SECONDS\n"
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

# put writes all of the data; false if it can't.
sub put {
  my ($fh, $data) = @_;
  my $off = 0;
  while ($off < length $data) {
    my $n = syswrite $fh, $data, length($data) - $off, $off;
    if (!defined $n) {
      next if $!{EINTR};
      return 0;
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
      if (sysread $fh, my $buf, MAX) { frame($n, DATA, $buf) }
      else { drop($n, 1) }
    }
  }
}
