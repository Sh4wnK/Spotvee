import { useCallback, useEffect, useState } from 'react';
import {
  ActivityIndicator,
  FlatList,
  StyleSheet,
  Text,
  TouchableOpacity,
  View,
} from 'react-native';

import { Api, DeviceInfo } from '../api/client';

interface Props {
  api: Api;
}

export default function DevicesScreen({ api }: Props) {
  const [devices, setDevices] = useState<DeviceInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      setDevices(await api.devices());
    } catch (e) {
      setError(e instanceof Error ? e.message : 'failed to load devices');
    } finally {
      setLoading(false);
    }
  }, [api]);

  useEffect(() => {
    void load();
  }, [load]);

  const toggle = (id: string) => {
    setDevices((prev) =>
      prev.map((d) => (d.id === id ? { ...d, selected: !d.selected } : d)),
    );
  };

  const save = async () => {
    setSaving(true);
    setError(null);
    try {
      const selected = devices.filter((d) => d.selected).map((d) => d.id);
      await api.selectDevices(selected);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'failed to save selection');
    } finally {
      setSaving(false);
    }
  };

  if (loading) {
    return (
      <View style={styles.center}>
        <ActivityIndicator size="large" />
      </View>
    );
  }

  return (
    <View style={styles.container}>
      <Text style={styles.title}>Select lights</Text>
      <Text style={styles.subtitle}>Choose the Govee devices to sync colors to</Text>

      <FlatList
        data={devices}
        keyExtractor={(d) => d.id}
        renderItem={({ item }) => (
          <TouchableOpacity style={styles.row} onPress={() => toggle(item.id)}>
            <View style={styles.rowText}>
              <Text style={styles.rowName}>{item.name}</Text>
              <Text style={styles.rowMeta}>
                {item.sku} · {item.id}
              </Text>
            </View>
            <View style={[styles.check, item.selected ? styles.checkOn : null]}>
              {item.selected ? <Text style={styles.checkMark}>✓</Text> : null}
            </View>
          </TouchableOpacity>
        )}
        ItemSeparatorComponent={() => <View style={styles.sep} />}
        ListEmptyComponent={<Text style={styles.empty}>No devices found.</Text>}
      />

      {error ? <Text style={styles.error}>{error}</Text> : null}

      <TouchableOpacity style={styles.button} onPress={save} disabled={saving}>
        {saving ? (
          <ActivityIndicator color="#fff" />
        ) : (
          <Text style={styles.buttonText}>Save selection</Text>
        )}
      </TouchableOpacity>
    </View>
  );
}

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: '#0d1117',
    padding: 16,
  },
  center: {
    flex: 1,
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: '#0d1117',
  },
  title: {
    color: '#e6edf3',
    fontSize: 24,
    fontWeight: '800',
    marginTop: 8,
  },
  subtitle: {
    color: '#8b949e',
    fontSize: 14,
    marginTop: 4,
    marginBottom: 16,
  },
  row: {
    backgroundColor: '#161b22',
    borderColor: '#30363d',
    borderWidth: 1,
    borderRadius: 10,
    padding: 16,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
  },
  rowText: {
    flex: 1,
  },
  rowName: {
    color: '#e6edf3',
    fontSize: 16,
    fontWeight: '600',
  },
  rowMeta: {
    color: '#8b949e',
    fontSize: 13,
    marginTop: 2,
  },
  check: {
    width: 26,
    height: 26,
    borderRadius: 13,
    borderWidth: 2,
    borderColor: '#30363d',
    alignItems: 'center',
    justifyContent: 'center',
  },
  checkOn: {
    backgroundColor: '#238636',
    borderColor: '#238636',
  },
  checkMark: {
    color: '#fff',
    fontSize: 15,
    fontWeight: '800',
  },
  sep: {
    height: 8,
  },
  empty: {
    color: '#8b949e',
    textAlign: 'center',
    marginTop: 40,
  },
  button: {
    backgroundColor: '#238636',
    borderRadius: 8,
    paddingVertical: 14,
    alignItems: 'center',
    marginTop: 16,
  },
  buttonText: {
    color: '#fff',
    fontSize: 16,
    fontWeight: '700',
  },
  error: {
    color: '#f85149',
    marginTop: 10,
    textAlign: 'center',
  },
});