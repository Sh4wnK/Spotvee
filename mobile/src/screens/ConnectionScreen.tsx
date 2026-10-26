import { useEffect, useState } from 'react';
import {
  ActivityIndicator,
  KeyboardAvoidingView,
  Platform,
  StyleSheet,
  Text,
  TextInput,
  TouchableOpacity,
  View,
} from 'react-native';

import { Api, getStoredToken, getStoredUrl, setStoredToken, setStoredUrl } from '../api/client';

interface Props {
  api: Api | null;
  onConnected: (api: Api, url: string, token: string) => void;
}

export default function ConnectionScreen({ api, onConnected }: Props) {
  const [url, setUrl] = useState('');
  const [token, setToken] = useState('');
  const [loading, setLoading] = useState(true);
  const [checking, setChecking] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [ready, setReady] = useState(false);

  // Load persisted settings on mount.
  useEffect(() => {
    (async () => {
      const [u, t] = await Promise.all([getStoredUrl(), getStoredToken()]);
      setUrl(u ?? 'http://192.168.1.10:8080');
      setToken(t ?? '');
      setLoading(false);
    })();
  }, []);

  const connect = async () => {
    const trimmedUrl = url.trim().replace(/\/+$/, '');
    const trimmedToken = token.trim();
    setError(null);
    setChecking(true);
    try {
      const candidate = new Api(trimmedUrl, trimmedToken);
      const ok = await candidate.health();
      if (!ok) throw new Error('server unreachable');
      await setStoredUrl(trimmedUrl);
      await setStoredToken(trimmedToken);
      onConnected(candidate, trimmedUrl, trimmedToken);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'connection failed');
    } finally {
      setChecking(false);
    }
  };

  useEffect(() => {
    setReady(url.trim().length > 0 && token.trim().length > 0);
  }, [url, token]);

  if (loading) {
    return (
      <View style={styles.center}>
        <ActivityIndicator size="large" />
      </View>
    );
  }

  return (
    <KeyboardAvoidingView
      style={styles.container}
      behavior={Platform.OS === 'ios' ? 'padding' : undefined}
    >
      <View style={styles.header}>
        <Text style={styles.title}>Spotvee</Text>
        <Text style={styles.subtitle}>Connect to your sync server</Text>
      </View>

      <Text style={styles.label}>Server URL</Text>
      <TextInput
        style={styles.input}
        value={url}
        onChangeText={setUrl}
        placeholder="http://192.168.1.10:8080"
        autoCapitalize="none"
        autoCorrect={false}
        keyboardType="url"
      />

      <Text style={styles.label}>App token (SPOTVEE_APP_TOKEN)</Text>
      <TextInput
        style={styles.input}
        value={token}
        onChangeText={setToken}
        placeholder="paste your app token"
        autoCapitalize="none"
        autoCorrect={false}
        secureTextEntry
      />

      {error ? <Text style={styles.error}>{error}</Text> : null}

      <TouchableOpacity
        style={[styles.button, !ready || checking ? styles.buttonDisabled : null]}
        disabled={!ready || checking}
        onPress={connect}
      >
        {checking ? (
          <ActivityIndicator color="#fff" />
        ) : (
          <Text style={styles.buttonText}>Connect</Text>
        )}
      </TouchableOpacity>

      {api ? (
        <TouchableOpacity style={styles.secondary} onPress={() => onConnected(api, url, token)}>
          <Text style={styles.secondaryText}>Skip (already configured)</Text>
        </TouchableOpacity>
      ) : null}
    </KeyboardAvoidingView>
  );
}

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: '#0d1117',
    padding: 24,
    justifyContent: 'center',
  },
  center: {
    flex: 1,
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: '#0d1117',
  },
  header: {
    marginBottom: 32,
  },
  title: {
    color: '#e6edf3',
    fontSize: 34,
    fontWeight: '800',
  },
  subtitle: {
    color: '#8b949e',
    fontSize: 15,
    marginTop: 6,
  },
  label: {
    color: '#8b949e',
    fontSize: 13,
    marginBottom: 6,
    marginTop: 14,
  },
  input: {
    backgroundColor: '#161b22',
    borderColor: '#30363d',
    borderWidth: 1,
    borderRadius: 8,
    color: '#e6edf3',
    paddingHorizontal: 12,
    paddingVertical: 12,
    fontSize: 15,
  },
  button: {
    backgroundColor: '#238636',
    borderRadius: 8,
    paddingVertical: 14,
    alignItems: 'center',
    marginTop: 24,
  },
  buttonDisabled: {
    opacity: 0.5,
  },
  buttonText: {
    color: '#fff',
    fontSize: 16,
    fontWeight: '700',
  },
  secondary: {
    marginTop: 16,
    alignItems: 'center',
  },
  secondaryText: {
    color: '#58a6ff',
    fontSize: 14,
  },
  error: {
    color: '#f85149',
    marginTop: 12,
    fontSize: 14,
  },
});