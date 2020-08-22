from django.urls import path

from . import views
from .views import ImportedData

urlpatterns = [
    path('', views.upload_file, name = 'upload_file'),
    path('success/', ImportedData.as_view(), name = 'upload_success'),
]