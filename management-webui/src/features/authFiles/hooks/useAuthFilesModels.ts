import { useCallback, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { authFilesApi } from '@/services/api';
import { useNotificationStore } from '@/stores';
import type { AuthFileItem } from '@/types';
import type { AuthFileModelItem } from '@/features/authFiles/constants';

type ModelsError = 'unsupported' | null;

export type UseAuthFilesModelsResult = {
  modelsModalOpen: boolean;
  modelsLoading: boolean;
  modelsList: AuthFileModelItem[];
  modelsFileName: string;
  modelsError: ModelsError;
  showModels: (item: AuthFileItem) => Promise<void>;
  closeModelsModal: () => void;
  invalidateModels: (names?: string[]) => void;
};

export function useAuthFilesModels(): UseAuthFilesModelsResult {
  const { t } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);

  const [modelsModalOpen, setModelsModalOpen] = useState(false);
  const [modelsLoading, setModelsLoading] = useState(false);
  const [modelsList, setModelsList] = useState<AuthFileModelItem[]>([]);
  const [modelsFileName, setModelsFileName] = useState('');
  const [modelsError, setModelsError] = useState<ModelsError>(null);
  const activeFileRef = useRef('');
  const activeModelsRequestIdRef = useRef(0);

  const closeModelsModal = useCallback(() => {
    activeFileRef.current = '';
    activeModelsRequestIdRef.current += 1;
    setModelsModalOpen(false);
    setModelsLoading(false);
  }, []);

  const loadModels = useCallback(
    async (fileName: string) => {
      const requestId = ++activeModelsRequestIdRef.current;
      const isRequestCurrent = () => requestId === activeModelsRequestIdRef.current;

      setModelsList([]);
      setModelsError(null);
      setModelsLoading(true);
      try {
        const models = await authFilesApi.getModelsForAuthFile(fileName, true);
        if (isRequestCurrent()) setModelsList(models);
      } catch (err) {
        if (!isRequestCurrent()) return;
        const errorMessage = err instanceof Error ? err.message : '';
        if (
          errorMessage.includes('404') ||
          errorMessage.includes('not found') ||
          errorMessage.includes('Not Found')
        ) {
          setModelsError('unsupported');
        } else {
          showNotification(`${t('notification.load_failed')}: ${errorMessage}`, 'error');
        }
      } finally {
        if (isRequestCurrent()) setModelsLoading(false);
      }
    },
    [showNotification, t]
  );

  const invalidateModels = useCallback(
    (names?: string[]) => {
      const fileName = activeFileRef.current;
      if (fileName && (!names || names.some((name) => name.trim() === fileName.trim()))) {
        void loadModels(fileName);
      }
    },
    [loadModels]
  );

  const showModels = useCallback(
    async (item: AuthFileItem) => {
      activeFileRef.current = item.name;
      setModelsFileName(item.name);
      setModelsModalOpen(true);
      await loadModels(item.name);
    },
    [loadModels]
  );

  return {
    modelsModalOpen,
    modelsLoading,
    modelsList,
    modelsFileName,
    modelsError,
    showModels,
    closeModelsModal,
    invalidateModels,
  };
}
