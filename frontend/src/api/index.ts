import axios from 'axios';
import { Task, Worker } from '../types';

const API_BASE_URL = 'http://localhost:8080/api/v1';

export const api = {
  // Task endpoints
  createTask: async (taskData: Omit<Task, 'id' | 'status' | 'created_at'>) => {
    const response = await axios.post(`${API_BASE_URL}/tasks`, taskData);
    return response.data;
  },

  getTasks: async () => {
    const response = await axios.get(`${API_BASE_URL}/tasks`);
    return response.data;
  },

  getTask: async (taskId: string) => {
    const response = await axios.get(`${API_BASE_URL}/tasks/${taskId}`);
    return response.data;
  },

  // Worker endpoints
  getWorkers: async () => {
    const response = await axios.get(`${API_BASE_URL}/workers`);
    return response.data;
  },

  getWorkerStatus: async (workerId: string) => {
    const response = await axios.get(`${API_BASE_URL}/workers/${workerId}/status`);
    return response.data;
  }
};
