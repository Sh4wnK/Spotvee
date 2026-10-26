import { useCallback, useEffect, useRef, useState } from 'react';
import {
  ActivityIndicator,
  Image,
  Linking,
  ScrollView,
  StyleSheet,
  Switch,
  Text,
  TouchableOpacity,
  View,
} from 'react-native';

import { Api, hexColor, Metrics, State } from '../api/client';

interface Props {
  api: Api;
  onStream: (state: State) => void;
  streamState: State | null;
}

export default function NowPlayingScreen({ api, onStream, streamState }: Props) {
  const [authenticated, setAuthenticated] = useState<boolean | null>(null);
  const [loginLoading, setLoginLoading] = useState(false);
  const [syncing, setSyncing] = useState(false);
  const [metrics, setMetrics] = useState<Metrics | null>(null);
  const streamRef = useRef<State | null>(streamState);
  streamRef.current = streamState;

  const state = streamState ?? {
    track: { id: '', name: '', artist: '', album: '', art_url: '', duration_ms: 0 },
    color: { dominant: { r: 0, g: 0, b: 0 }, palette: [], weights: [] },
    syncing: false,
    updated_at: '',
  };

  const loadAuth = useCallback(async () => {
    try {
      setAuthenticated(await api.authStatus());
    } catch {
      setAuthenticated(false);
    }
  }, [api]);

  useEffect(() => {
    void loadAuth();
  }, [loadAuth]);

  useEffect(() => {
    onStream(state); // keep parent streamState fresh via callback
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const openLogin = async () => {
    setLoginLoading(true);
    try {
      const url = await api.loginUrl();
      await Linking.openURL(url);
    } catch {
      // fall through
    } finally {
      setLoginLoading(false);
    }
  };

  const toggleSync = async (value: boolean) => {
    setSyncing(value);
    try {
      await api.setSync(value, false);
    } catch {
      // revert on failure
      setSyncing(!value);
    }
  };

  const refreshMetrics = useCallback(async () => {
    try {
      setMetrics(await api.metrics());
    } catch {
      // metrics may be disabled; ignore
    }
  }, [api]);

  useEffect(() => {
    void refreshMetrics();
    const iv = setInterval(refreshMetrics, 5000);
    return () => clearInterval(iv);
  }, [refreshMetrics]);

  const dominant = state.color.dominant;
  const dominantHex = hexColor(dominant);
  const accent = (state.color.palette ?? []).slice(0, 4);

  return (
    <ScrollView style={styles.container} contentContainerStyle={styles.content}>
      <Text style={styles.title}>Now playing</Text>

      {authenticated === false ? (
        <View style={styles.authBanner}>
          <Text style={styles.authText}>Connect your Spotify account to start syncing.</Text>
          <TouchableOpacity style={styles.smallButton} onPress={openLogin} disabled={loginLoading}>
            {loginLoading ? (
              <ActivityIndicator color="#fff" />
            ) : (
              <Text style={styles.smallButtonText}>Connect Spotify</Text>
            )}
          </TouchableOpacity>
        </View>
      ) : (
        <TouchableOpacity style={styles.connected} onPress={() => void loadAuth()}>
          <View style={styles.greenDot} />
          <Text style={styles.connectedText}>Spotify connected</Text>
        </TouchableOpacity>
      )}

      <View style={styles.artRow}>
        {state.track.art_url ? (
          <Image source={{ uri: state.track.art_url }} style={styles.art} />
        ) : (
          <View style={[styles.art, styles.artPlaceholder]}>
            <Text style={styles.artPlaceholderText}>♪</Text>
          </View>
        )}
        <View style={styles.trackInfo}>
          <Text style={styles.trackName}>{state.track.name || 'Nothing playing'}</Text>
          <Text style={styles.trackArtist}>{state.track.artist}</Text>
          <Text style={styles.trackAlbum}>{state.track.album}</Text>
        </View>
      </View>

      <View style={styles.colorRow}>
        <View style={styles.dominantWrap}>
          <Text style={styles.colorLabel}>Dominant color</Text>
          <View style={[styles.swatch, { backgroundColor: dominantHex }]} />
          <Text style={styles.hexText}>{dominantHex.toUpperCase()}</Text>
        </View>
        <View style={styles.paletteWrap}>
          <Text style={styles.colorLabel}>Palette</Text>
          <View style={styles.paletteRow}>
            {accent.map((c, i) => (
              <View
                key={`${c.r}${c.g}${c.b}${i}`}
                style={[styles.paletteSwatch, { backgroundColor: hexColor(c) }]}
              />
            ))}
            {accent.length === 0 ? <Text style={styles.noPalette}>—</Text> : null}
          </View>
        </View>
      </View>

      {state.error ? <Text style={styles.stateError}>{state.error}</Text> : null}

      <View style={styles.syncRow}>
        <View>
          <Text style={styles.syncLabel}>Color sync</Text>
          <Text style={styles.syncHint}>Push album-art colors to your lights</Text>
        </View>
        <Switch
          value={syncing}
          onValueChange={toggleSync}
          trackColor={{ false: '#30363d', true: '#238636' }}
        />
      </View>

      <Text style={styles.sectionTitle}>Latency (track change → lights acked)</Text>
      {metrics && metrics.count > 0 ? (
        <View style={styles.metricsGrid}>
          <Metric label="p50" value={`${metrics.p50.toFixed(1)} ms`} />
          <Metric label="p95" value={`${metrics.p95.toFixed(1)} ms`} />
          <Metric label="p99" value={`${metrics.p99.toFixed(1)} ms`} />
          <Metric label="samples" value={String(metrics.count)} />
        </View>
      ) : (
        <Text style={styles.noMetrics}>No sync events yet — play a track.</Text>
      )}
    </ScrollView>
  );
}

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <View style={styles.metricCell}>
      <Text style={styles.metricValue}>{value}</Text>
      <Text style={styles.metricLabel}>{label}</Text>
    </View>
  );
}

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: '#0d1117',
  },
  content: {
    padding: 16,
    paddingBottom: 48,
  },
  title: {
    color: '#e6edf3',
    fontSize: 28,
    fontWeight: '800',
    marginTop: 8,
  },
  connected: {
    flexDirection: 'row',
    alignItems: 'center',
    marginTop: 14,
    marginBottom: 14,
  },
  greenDot: {
    width: 10,
    height: 10,
    borderRadius: 5,
    backgroundColor: '#3fb950',
    marginRight: 8,
  },
  connectedText: {
    color: '#3fb950',
    fontSize: 14,
    fontWeight: '600',
  },
  authBanner: {
    backgroundColor: '#1c2128',
    borderRadius: 10,
    padding: 16,
    marginTop: 14,
    marginBottom: 14,
  },
  authText: {
    color: '#e6edf3',
    fontSize: 14,
    marginBottom: 12,
  },
  smallButton: {
    backgroundColor: '#1f6feb',
    borderRadius: 8,
    paddingVertical: 10,
    alignItems: 'center',
    alignSelf: 'flex-start',
    paddingHorizontal: 18,
  },
  smallButtonText: {
    color: '#fff',
    fontWeight: '700',
    fontSize: 14,
  },
  artRow: {
    flexDirection: 'row',
    alignItems: 'center',
    marginTop: 12,
  },
  art: {
    width: 120,
    height: 120,
    borderRadius: 12,
  },
  artPlaceholder: {
    backgroundColor: '#161b22',
    alignItems: 'center',
    justifyContent: 'center',
  },
  artPlaceholderText: {
    color: '#8b949e',
    fontSize: 48,
  },
  trackInfo: {
    flex: 1,
    marginLeft: 16,
  },
  trackName: {
    color: '#e6edf3',
    fontSize: 18,
    fontWeight: '800',
  },
  trackArtist: {
    color: '#8b949e',
    fontSize: 15,
    marginTop: 4,
  },
  trackAlbum: {
    color: '#6e7681',
    fontSize: 13,
    marginTop: 2,
  },
  colorRow: {
    flexDirection: 'row',
    marginTop: 24,
    gap: 24,
  },
  dominantWrap: {
    alignItems: 'flex-start',
  },
  paletteWrap: {
    flex: 1,
  },
  colorLabel: {
    color: '#8b949e',
    fontSize: 12,
    textTransform: 'uppercase',
    letterSpacing: 1,
    marginBottom: 8,
  },
  swatch: {
    width: 72,
    height: 72,
    borderRadius: 12,
    borderWidth: 1,
    borderColor: '#30363d',
  },
  hexText: {
    color: '#e6edf3',
    fontSize: 14,
    fontWeight: '600',
    marginTop: 8,
  },
  paletteRow: {
    flexDirection: 'row',
    flexWrap: 'wrap',
    gap: 8,
  },
  paletteSwatch: {
    width: 40,
    height: 40,
    borderRadius: 8,
    borderWidth: 1,
    borderColor: '#30363d',
  },
  noPalette: {
    color: '#6e7681',
    fontSize: 18,
  },
  stateError: {
    color: '#f85149',
    fontSize: 13,
    marginTop: 12,
  },
  syncRow: {
    backgroundColor: '#161b22',
    borderRadius: 12,
    padding: 16,
    marginTop: 24,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
  },
  syncLabel: {
    color: '#e6edf3',
    fontSize: 16,
    fontWeight: '700',
  },
  syncHint: {
    color: '#8b949e',
    fontSize: 13,
    marginTop: 2,
  },
  sectionTitle: {
    color: '#e6edf3',
    fontSize: 16,
    fontWeight: '700',
    marginTop: 28,
    marginBottom: 10,
  },
  metricsGrid: {
    flexDirection: 'row',
    flexWrap: 'wrap',
    gap: 10,
  },
  metricCell: {
    backgroundColor: '#161b22',
    borderRadius: 10,
    padding: 12,
    minWidth: 100,
  },
  metricValue: {
    color: '#e6edf3',
    fontSize: 18,
    fontWeight: '800',
    fontVariant: ['tabular-nums'],
  },
  metricLabel: {
    color: '#8b949e',
    fontSize: 12,
    marginTop: 2,
  },
  noMetrics: {
    color: '#6e7681',
    fontSize: 14,
  },
});