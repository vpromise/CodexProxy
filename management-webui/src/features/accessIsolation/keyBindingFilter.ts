import type { IsolationSettings } from './types';

export interface KeyBindingFilterState {
  search: string;
  filter: string;
  retainedKeys: string[];
}

type KeyBindingFilterAction =
  | { type: 'search'; value: string }
  | { type: 'filter'; value: string }
  | { type: 'edit'; key: string };

export const initialKeyBindingFilter: KeyBindingFilterState = {
  search: '',
  filter: 'all',
  retainedKeys: [],
};

export function keyBindingFilterReducer(
  state: KeyBindingFilterState,
  action: KeyBindingFilterAction
): KeyBindingFilterState {
  if (action.type === 'edit') {
    return state.retainedKeys.includes(action.key)
      ? state
      : { ...state, retainedKeys: [...state.retainedKeys, action.key] };
  }
  // Keep edited rows mounted until the user deliberately changes the search or filter.
  return { ...state, [action.type]: action.value, retainedKeys: [] };
}

export function getVisibleBindingKeys(
  settings: IsolationSettings,
  apiKeys: string[],
  state: KeyBindingFilterState
): string[] {
  const allKeys = [...new Set([...apiKeys, ...settings.keys.map((binding) => binding.key)])];
  const query = state.search.trim().toLocaleLowerCase();
  return allKeys.filter((key) => {
    if (state.retainedKeys.includes(key)) return true;
    const binding = settings.keys.find((item) => item.key === key);
    const pool = settings.pools.find((item) => item.id === binding?.pool);
    return (
      `${key.slice(-4)} ${binding?.name ?? ''} ${binding?.note ?? ''} ${pool?.name ?? ''}`
        .toLocaleLowerCase()
        .includes(query) &&
      (state.filter === 'all' ||
        (state.filter === 'unassigned' ? apiKeys.includes(key) && !pool : !apiKeys.includes(key)))
    );
  });
}
