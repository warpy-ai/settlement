import { useEffect, useState } from 'react';
import { Box, Container, Tab, Tabs, Typography, Paper } from '@mui/material';
import { TaskForm } from './components/TaskForm';
import { TaskList } from './components/TaskList';
import { WorkerGraph } from './components/WorkerGraph';
import { Task, Worker } from './types';
import { api } from './api';

interface TabPanelProps {
  children?: React.ReactNode;
  index: number;
  value: number;
}

function TabPanel(props: TabPanelProps) {
  const { children, value, index, ...other } = props;

  return (
    <div
      role="tabpanel"
      hidden={value !== index}
      {...other}
    >
      {value === index && (
        <Box sx={{ p: 3 }}>
          {children}
        </Box>
      )}
    </div>
  );
}

function App() {
  const [tabValue, setTabValue] = useState(0);
  const [tasks, setTasks] = useState<Task[]>([]);
  const [workers, setWorkers] = useState<Worker[]>([]);

  const fetchData = async () => {
    try {
      const [tasksData, workersData] = await Promise.all([
        api.getTasks(),
        api.getWorkers()
      ]);
      setTasks(tasksData);
      setWorkers(workersData);
    } catch (error) {
      console.error('Error fetching data:', error);
    }
  };

  useEffect(() => {
    fetchData();
    // Poll for updates every 5 seconds
    const interval = setInterval(fetchData, 5000);
    return () => clearInterval(interval);
  }, []);

  const handleTabChange = (_: React.SyntheticEvent, newValue: number) => {
    setTabValue(newValue);
  };

  // Create a mapping of taskId to task content for the graph
  const taskContentMap = tasks.reduce((acc, task) => {
    acc[task.id] = task.content;
    return acc;
  }, {} as { [key: string]: string });

  return (
    <Container maxWidth="lg" sx={{ mt: 4 }}>
      <Typography variant="h4" component="h1" gutterBottom>
        Settlement Task Manager
      </Typography>

      <Paper sx={{ width: '100%', mb: 2 }}>
        <Tabs
          value={tabValue}
          onChange={handleTabChange}
          indicatorColor="primary"
          textColor="primary"
        >
          <Tab label="Create Task" />
          <Tab label="Task List" />
          <Tab label="Worker Visualization" />
        </Tabs>

        <TabPanel value={tabValue} index={0}>
          <TaskForm />
        </TabPanel>

        <TabPanel value={tabValue} index={1}>
          <TaskList tasks={tasks} />
        </TabPanel>

        <TabPanel value={tabValue} index={2}>
          <WorkerGraph workers={workers} tasks={taskContentMap} />
        </TabPanel>
      </Paper>
    </Container>
  );
}

export default App;
