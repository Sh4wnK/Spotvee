import { StatusBar } from 'expo-status-bar';
import { useCallback, useEffect, useRef, useState } from 'react';
import { StyleSheet, Text, TouchableOpacity, View } from 'react-native';

import { Api, State, StateStream, getStoredToken, getStoredUrl } from './src/api/client';
import ConnectionScreen from './src/screens/ConnectionScreen';
import DevicesScreen from './src/screens/DevicesScreen';
import NowPlayingScreen from './src/screens/NowPlayingScreen';

type Screen = 'now' | 'devices' | 'connection';

export default function App() {
  const [api, setApi] = useState<Api | null>(null);
  const [screen, setScreen] = useState<Screen>('connection');
  const [streamState, setStreamState] = useState<State | null>(null);
  const streamRef = useRef<StateStream | null>(null);

  // Restore a persisted session on first launch.
  useEffect(() => {
    (async () => {
      const [url, token] = await Promise.all([getStoredUrl(), getStoredToken()]);
      if (url && token) {
        const candidate = new Api(url, token);
        if (await candidate.health()) {
          setApi(candidate);
          setScreen('now');
          attachStream(candidate);
        }
      }
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const attachStream = useCallback((client: Api) => {
    streamRef.current?.close();
    const stream = new StateStream(client.baseUrl, client.getToken());
    stream.onState(setStreamState);
    stream.connect();
    streamRef.current = stream;
  }, []);

  const handleConnected = (client: Api) => {
    setApi(client);
    setScreen('now');
    attachStream(client);
  };

  return (
    <View style={styles.root}>
      <StatusBar style="light" />

      {api ? (
        <>
          <View style={styles.tabBar}>
            <TabButton label="Now" active={screen === 'now'} onPress={() => setScreen('now')} />
            <TabButton
              label="Devices"
              active={screen === 'devices'}
              onPress={() => setScreen('devices')}
            />
            <TabButton
              label="Settings"
              active={screen === 'connection'}
              onPress={() => setScreen('connection')}
            />
          </View>

          {screen === 'now' ? <NowPlayingScreen api={api} streamState={streamState} /> : null}
          {screen === 'devices' ? <DevicesScreen api={api} /> : null}
          {screen === 'connection' ? (
            <ConnectionScreen api={api} onConnected={handleConnected} />
          ) : null}
        </>
      ) : (
        <ConnectionScreen api={null} onConnected={handleConnected} />
      )}
    </View>
  );
}

function TabButton({
  label,
  active,
  onPress,
}: {
  label: string;
  active: boolean;
  onPress: () => void;
}) {
  return (
    <TouchableOpacity style={[styles.tab, active ? styles.tabActive : null]} onPress={onPress}>
      <Text style={[styles.tabLabel, active ? styles.tabLabelActive : null]}>{label}</Text>
    </TouchableOpacity>
  );
}

const styles = StyleSheet.create({
  root: {
    flex: 1,
    backgroundColor: '#0d1117',
  },
  tabBar: {
    flexDirection: 'row',
    backgroundColor: '#161b22',
    borderBottomWidth: 1,
    borderBottomColor: '#30363d',
  },
  tab: {
    flex: 1,
    alignItems: 'center',
    paddingVertical: 14,
  },
  tabActive: {
    borderBottomWidth: 2,
    borderBottomColor: '#238636',
  },
  tabLabel: {
    color: '#8b949e',
    fontSize: 15,
    fontWeight: '600',
  },
  tabLabelActive: {
    color: '#e6edf3',
  },
});